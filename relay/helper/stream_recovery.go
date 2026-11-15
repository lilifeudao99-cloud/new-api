package helper

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const streamRecoverySessionKey = "stream_recovery_session"
const maxStreamPreludeBytes = 1 << 20

// RecoveryStreamSession owns the pre-content heartbeat across ALL attempts.
// It stops and joins the writer goroutine before handing a response to an
// adaptor, so response header copying and normal streaming cannot race it.
type RecoveryStreamSession struct {
	state  *relaycommon.StreamRecovery
	writer *recoveryStreamWriter
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

type recoveryStreamWriter struct {
	gin.ResponseWriter
	mu sync.Mutex
}

func (w *recoveryStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *recoveryStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseWriter.Write(p)
}
func (w *recoveryStreamWriter) WriteString(p string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseWriter.WriteString(p)
}
func (w *recoveryStreamWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ResponseWriter.WriteHeader(code)
}
func (w *recoveryStreamWriter) Flush() { w.mu.Lock(); defer w.mu.Unlock(); w.ResponseWriter.Flush() }

func StartStreamRecovery(c *gin.Context, info *relaycommon.RelayInfo, policy relaycommon.StreamRecoveryPolicy) *RecoveryStreamSession {
	info.StreamRecovery = relaycommon.NewStreamRecovery(info.StartTime, policy)
	s := &RecoveryStreamSession{state: info.StreamRecovery, writer: &recoveryStreamWriter{ResponseWriter: c.Writer}, stop: make(chan struct{}), done: make(chan struct{})}
	c.Writer = s.writer
	c.Set(streamRecoverySessionKey, s)
	SetEventStreamHeaders(c)
	requestContext := c.Request.Context()
	disablePing := info.DisablePing
	go func() {
		defer close(s.done)
		defer func() {
			if recover() != nil {
				s.state.AbortWaiting(relaycommon.ErrStreamClientWrite)
			}
		}()
		interval := policy.HeartbeatInterval
		if interval <= 0 {
			interval = DefaultPingInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-requestContext.Done():
				s.state.Observe("client_cancelled", 0, "", 0)
				return
			case <-ticker.C:
				if disablePing {
					continue
				}
				ExtendWriteDeadline(c)
				if err := PingData(c); err != nil {
					s.state.AbortWaiting(relaycommon.ErrStreamClientWrite)
					return
				}
				s.state.Observe("heartbeat", 0, "", 0)
			}
		}
	}()
	return s
}

func (s *RecoveryStreamSession) StopWaiting() {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.stop) })
	<-s.done
}

func StopStreamRecoveryWait(c *gin.Context) {
	if value, ok := c.Get(streamRecoverySessionKey); ok {
		value.(*RecoveryStreamSession).StopWaiting()
	}
}

func GetStreamRecovery(c *gin.Context) *relaycommon.StreamRecovery {
	if value, ok := c.Get(streamRecoverySessionKey); ok {
		return value.(*RecoveryStreamSession).state
	}
	return nil
}

// After a heartbeat, HTTP 200 is already committed. Exhaustion is an SSE error,
// never a JSON document appended to the stream. Mid-output errors are not
// replayed; providers retain their usual streaming behavior after release.
func (s *RecoveryStreamSession) WriteError(c *gin.Context, err *types.NewAPIError, format types.RelayFormat) bool {
	if s == nil {
		return false
	}
	s.StopWaiting()
	if !c.Writer.Written() {
		c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		c.Writer.Header().Del("Transfer-Encoding")
		c.Writer.Header().Del("X-Accel-Buffering")
		return false
	}
	if c.Request.Context().Err() != nil {
		return true
	}
	ExtendWriteDeadline(c)
	var payload any = gin.H{"error": err.ToOpenAIError()}
	if format == types.RelayFormatClaude {
		payload = gin.H{"type": "error", "error": err.ToClaudeError()}
	} else if format == types.RelayFormatOpenAIResponses {
		openaiErr := err.ToOpenAIError()
		payload = gin.H{"type": "error", "code": openaiErr.Code, "message": openaiErr.Message, "param": openaiErr.Param}
	}
	data, marshalErr := common.Marshal(payload)
	if marshalErr == nil {
		_, _ = c.Writer.Write([]byte("event: error\ndata: " + string(data) + "\n\n"))
		_ = FlushWriter(c)
	}
	return true
}

type streamReplayBody struct {
	io.Reader
	io.Closer
}

// PrepareRecoverableStream reads only the small SSE prelude. It preserves the
// exact bytes (including event names and multiline data) for the existing
// adaptor. No response text is logged or written before it is usable.
func PrepareRecoverableStream(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) error {
	s := info.StreamRecovery
	if s == nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		// Unknown streaming protocols cannot be replayed safely.
		if !s.AcceptContent() {
			return StreamRecoveryTransportError(c, s, s.Context().Err())
		}
		StopStreamRecoveryWait(c)
		return nil
	}
	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	var prelude bytes.Buffer
	var eventData strings.Builder
	var partialLine strings.Builder
	for {
		line, readErr := reader.ReadSlice('\n')
		if prelude.Len()+len(line) > maxStreamPreludeBytes {
			return types.NewErrorWithStatusCode(errors.New("upstream stream prelude exceeds limit"), "stream_prelude_limit", http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
		prelude.Write(line)
		if readErr == bufio.ErrBufferFull {
			partialLine.Write(line)
			continue
		}
		text := strings.TrimRight(string(line), "\r\n")
		if partialLine.Len() > 0 {
			partialLine.WriteString(text)
			text = partialLine.String()
			partialLine.Reset()
		}
		if value, ok := strings.CutPrefix(text, "data:"); ok {
			if eventData.Len() > 0 {
				eventData.WriteByte('\n')
			}
			eventData.WriteString(strings.TrimPrefix(value, " "))
		}
		if (text == "" || readErr != nil) && eventData.Len() > 0 {
			s.Observe("event", 0, "", 0)
			usable, apiErr := classifyStreamPrelude(eventData.String())
			eventData.Reset()
			if apiErr != nil {
				return apiErr
			}
			if usable {
				// Do not release if the deadline/client cancellation won the race.
				if !s.AcceptContent() {
					return StreamRecoveryTransportError(c, s, s.Context().Err())
				}
				StopStreamRecoveryWait(c)
				resp.Body = &streamReplayBody{Reader: io.MultiReader(bytes.NewReader(prelude.Bytes()), reader), Closer: resp.Body}
				return nil
			}
		}
		if readErr != nil {
			if s.Context().Err() != nil {
				return StreamRecoveryTransportError(c, s, readErr)
			}
			if errors.Is(readErr, io.EOF) {
				return types.NewErrorWithStatusCode(errors.New("upstream ended before usable output"), "stream_empty", http.StatusBadGateway)
			}
			return StreamRecoveryTransportError(c, s, readErr)
		}
	}
}

// classifyStreamPrelude is conservative for unknown events: expose them once,
// rather than risk replaying an unrecognized tool action. Known bookkeeping
// events do not count as content. Refusals and parameter/policy errors stop.
func classifyStreamPrelude(data string) (bool, *types.NewAPIError) {
	if strings.TrimSpace(data) == "[DONE]" {
		return false, types.NewErrorWithStatusCode(errors.New("upstream ended before usable output"), "stream_empty", http.StatusBadGateway)
	}
	if !gjson.Valid(data) {
		return false, types.NewErrorWithStatusCode(errors.New("invalid upstream SSE data"), types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	j := gjson.Parse(data)
	typ := j.Get("type").String()
	if j.Get("error").Exists() || typ == "error" || typ == "response.failed" || typ == "response.incomplete" || typ == "response.cancelled" || typ == "response.canceled" {
		errorObject := j.Get("error")
		if !errorObject.Exists() {
			errorObject = j.Get("response.error")
		}
		code := errorObject.Get("code").String()
		if code == "" {
			code = errorObject.Get("type").String()
		}
		status := http.StatusBadRequest
		switch code {
		case "rate_limit_exceeded", "rate_limit_error":
			status = http.StatusTooManyRequests
		case "server_error", "internal_server_error", "overloaded_error", "service_unavailable":
			status = http.StatusServiceUnavailable
		}
		if !relaycommon.IsTransientStreamError(types.NewErrorWithStatusCode(errors.New(errorObject.Get("message").String()), types.ErrorCode(code), status)) {
			status = http.StatusBadRequest
		}
		if status == http.StatusBadRequest {
			return false, types.NewErrorWithStatusCode(errors.New("上游未完成本次请求，请检查请求内容或参数后重试"), "stream_request_rejected", status, types.ErrOptionWithSkipRetry())
		}
		return false, types.NewErrorWithStatusCode(errors.New("上游暂时无法完成请求"), "stream_upstream_unavailable", status)
	}
	if strings.HasPrefix(typ, "response.") {
		switch typ {
		case "response.created", "response.queued", "response.in_progress", "response.content_part.added", "response.reasoning_summary_part.added":
			return false, nil
		case "response.output_item.added":
			item := j.Get("item.type").String()
			return item != "message" && item != "reasoning" || streamOutputHasContent(j.Get("item")), nil
		case "response.completed", "response.done":
			for _, output := range j.Get("response.output").Array() {
				if streamOutputHasContent(output) {
					return true, nil
				}
			}
			return false, types.NewErrorWithStatusCode(errors.New("upstream completed without output"), "stream_empty", http.StatusBadGateway)
		default:
			if strings.HasSuffix(typ, ".delta") {
				return j.Get("delta").String() != "", nil
			}
			return true, nil
		}
	}
	if choices := j.Get("choices"); choices.Exists() {
		for _, choice := range choices.Array() {
			for _, field := range []string{"delta.content", "delta.reasoning_content", "delta.reasoning", "delta.refusal", "text"} {
				if v := choice.Get(field); v.Exists() && v.String() != "" {
					return true, nil
				}
			}
			if choice.Get("delta.tool_calls").Exists() || choice.Get("delta.function_call").Exists() {
				return true, nil
			}
			if reason := choice.Get("finish_reason").String(); reason != "" {
				if reason == "content_filter" {
					return false, types.NewErrorWithStatusCode(errors.New("上游拒绝了本次请求"), types.ErrorCodePromptBlocked, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
				}
				return false, types.NewErrorWithStatusCode(errors.New("upstream completed without output"), "stream_empty", http.StatusBadGateway)
			}
		}
		return false, nil
	}
	switch typ {
	case "ping", "message_start":
		return false, nil
	case "content_block_start":
		block := j.Get("content_block")
		return block.Get("type").String() != "text" && block.Get("type").String() != "thinking" || block.Get("text").String() != "" || block.Get("thinking").String() != "", nil
	case "content_block_delta":
		return j.Get("delta.text").String() != "" || j.Get("delta.thinking").String() != "" || j.Get("delta.partial_json").String() != "", nil
	case "message_delta", "message_stop":
		return false, types.NewErrorWithStatusCode(errors.New("upstream completed without output"), "stream_empty", http.StatusBadGateway)
	}
	return true, nil
}

func streamOutputHasContent(item gjson.Result) bool {
	switch item.Get("type").String() {
	case "message", "reasoning":
		for _, field := range []string{"content", "summary"} {
			for _, part := range item.Get(field).Array() {
				if part.Get("text").String() != "" || part.Get("refusal").String() != "" {
					return true
				}
			}
		}
		return false
	case "":
		return false
	default:
		return true // tool or unfamiliar side effect: never replay
	}
}

func StreamRecoveryTransportError(c *gin.Context, s *relaycommon.StreamRecovery, err error) *types.NewAPIError {
	if c.Request.Context().Err() != nil {
		s.Observe("client_cancelled", 0, "", 0)
		return types.NewErrorWithStatusCode(errors.New("client connection cancelled"), "stream_client_cancelled", 499, types.ErrOptionWithSkipRetry())
	}
	cause := context.Cause(s.Context())
	if errors.Is(cause, relaycommon.ErrStreamClientWrite) {
		return types.NewErrorWithStatusCode(cause, "stream_downstream_write_failed", 499, types.ErrOptionWithSkipRetry())
	}
	if errors.Is(cause, relaycommon.ErrStreamRecoveryBudget) {
		return types.NewErrorWithStatusCode(cause, "stream_recovery_budget", http.StatusGatewayTimeout, types.ErrOptionWithSkipRetry())
	}
	if errors.Is(cause, relaycommon.ErrFirstContentTimeout) {
		return types.NewErrorWithStatusCode(cause, "stream_first_content_timeout", http.StatusGatewayTimeout)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return types.NewErrorWithStatusCode(errors.New("upstream transport timed out"), "stream_transport_timeout", http.StatusGatewayTimeout)
	}
	// Transport errors can contain credential-bearing URLs. Only the fixed
	// classification is persisted; the underlying message is not propagated.
	return types.NewErrorWithStatusCode(errors.New("upstream connection interrupted"), "stream_connection_interrupted", http.StatusBadGateway)
}

func ObserveStreamRecoveryTerminal(info *relaycommon.RelayInfo, data string) {
	if info.StreamRecovery == nil {
		return
	}
	event := ""
	switch gjson.Get(data, "type").String() {
	case "response.completed", "response.done", "message_stop":
		event = "completed"
	case "response.failed", "error":
		event = "upstream_failed"
	case "response.incomplete":
		event = "upstream_incomplete"
	case "response.cancelled", "response.canceled":
		event = "upstream_cancelled"
	}
	if event != "" {
		info.StreamRecovery.Observe(event, 0, "", 0)
	}
}
