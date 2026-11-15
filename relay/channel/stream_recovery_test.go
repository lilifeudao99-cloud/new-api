package channel

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recoveryTestContext(t *testing.T, policy relaycommon.StreamRecoveryPolicy, parents ...context.Context) (*gin.Context, *relaycommon.RelayInfo, *helper.RecoveryStreamSession) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"private prompt"}`))
	if len(parents) > 0 {
		c.Request = c.Request.WithContext(parents[0])
	}
	info := &relaycommon.RelayInfo{IsStream: true, StartTime: time.Now(), ChannelMeta: &relaycommon.ChannelMeta{}}
	session := helper.StartStreamRecovery(c, info, policy)
	t.Cleanup(session.StopWaiting)
	t.Cleanup(func() { info.StreamRecovery.EndAttempt("test_complete", false) })
	require.True(t, info.StreamRecovery.BeginAttempt(c.Request.Context(), 31))
	return c, info, session
}

func recoveryTestPolicy() relaycommon.StreamRecoveryPolicy {
	return relaycommon.StreamRecoveryPolicy{FirstContentTimeout: 5 * time.Second, WaitBudget: 10 * time.Second, MaxAttempts: 3, HeartbeatInterval: time.Hour}
}

func TestStreamRecoveryPreludePreservesProtocolsAndStopsReplay(t *testing.T) {
	tests := []struct {
		name, prelude, content, code string
		retry                        bool
	}{
		{"responses text", `{"type":"response.created","response":{"id":"discarded"}}`, `{"type":"response.output_text.delta","delta":"hello"}`, "", false},
		{"responses reasoning", `{"type":"response.in_progress"}`, `{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`, "", false},
		{"responses tool", `{"type":"response.created"}`, `{"type":"response.output_item.added","item":{"type":"function_call","name":"save"}}`, "", false},
		{"chat role", `{"choices":[{"delta":{"role":"assistant"}}]}`, `{"choices":[{"delta":{"content":"hello"}}]}`, "", false},
		{"chat tool", `{"choices":[{"delta":{"role":"assistant"}}]}`, `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"save"}}]}}]}`, "", false},
		{"claude text", `{"type":"message_start"}`, `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`, "", false},
		{"claude tool", `{"type":"message_start"}`, `{"type":"content_block_start","content_block":{"type":"tool_use","name":"save"}}`, "", false},
		{"refusal is output", `{"type":"response.created"}`, `{"type":"response.refusal.delta","delta":"cannot comply"}`, "", false},
		{"empty done", `{"type":"response.created"}`, `[DONE]`, "stream_empty", true},
		{"empty eof", `{"type":"response.created"}`, "", "stream_empty", true},
		{"one token without output", `{"type":"response.created"}`, `{"type":"response.completed","response":{"usage":{"output_tokens":1},"output":[]}}`, "stream_empty", true},
		{"terminal text", `{"type":"response.created"}`, `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"complete"}]}]}}`, "", false},
		{"transient SSE failure", `{"type":"response.created"}`, `{"type":"response.failed","response":{"error":{"code":"server_error","message":"private upstream text"}}}`, "stream_upstream_unavailable", true},
		{"quota SSE failure", `{"type":"response.created"}`, `{"type":"response.failed","response":{"error":{"code":"insufficient_quota"}}}`, "stream_request_rejected", false},
		{"quota disguised as rate limit", `{"type":"response.created"}`, `{"type":"error","error":{"type":"rate_limit_error","message":"insufficient credits"}}`, "stream_request_rejected", false},
		{"incomplete limit", `{"type":"response.created"}`, `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, "stream_request_rejected", false},
		{"policy SSE failure", `{"type":"response.created"}`, `{"type":"error","error":{"code":"content_policy_violation"}}`, "stream_request_rejected", false},
		{"malformed", `{"type":"response.created"}`, `not json`, "bad_response_body", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, info, _ := recoveryTestContext(t, recoveryTestPolicy())
			body := "event: test\ndata: " + tt.prelude + "\n\n"
			if tt.content != "" {
				body += "event: test\ndata: " + tt.content + "\n\n"
			}
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
			err := helper.PrepareRecoverableStream(c, resp, info)
			if tt.code != "" {
				var apiErr *types.NewAPIError
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.code, string(apiErr.GetErrorCode()))
				assert.Equal(t, tt.retry, relaycommon.IsTransientStreamError(apiErr))
				assert.False(t, info.StreamRecovery.ContentStarted())
				assert.NotContains(t, apiErr.Error(), "private upstream text")
			} else {
				require.NoError(t, err)
				got, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assert.Equal(t, body, string(got), "the adaptor must receive the exact original SSE bytes")
				assert.True(t, info.StreamRecovery.ContentStarted())
				assert.False(t, info.StreamRecovery.CanRetry(), "text, reasoning and tools must never be replayed")
			}
			assert.False(t, c.Writer.Written(), "prelude must stay private until the adaptor writes it")
			encoded, err := common.Marshal(info.StreamRecovery.Snapshot())
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "private prompt")
			assert.NotContains(t, string(encoded), "discarded")
			assert.NotContains(t, string(encoded), "private upstream text")
		})
	}
}

func TestStreamRecoveryExhaustionReturnsSSEOnlyAfterHeartbeat(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatOpenAIResponses, types.RelayFormatClaude} {
		t.Run(string(format), func(t *testing.T) {
			c, info, session := recoveryTestContext(t, recoveryTestPolicy())
			// Exercise the same comment write as the ticker without waiting for
			// a wall clock, then exhaust the configured number of attempts.
			require.NoError(t, helper.PingData(c))
			info.StreamRecovery.EndAttempt("upstream_error", true)
			require.True(t, info.StreamRecovery.BeginAttempt(c.Request.Context(), 19))
			info.StreamRecovery.EndAttempt("upstream_error", true)
			require.True(t, info.StreamRecovery.BeginAttempt(c.Request.Context(), 31))
			info.StreamRecovery.EndAttempt("upstream_error", false)
			assert.False(t, info.StreamRecovery.CanRetry())
			assert.False(t, info.StreamRecovery.BeginAttempt(c.Request.Context(), 19))
			err := types.NewErrorWithStatusCode(errors.New("temporarily unavailable (request id: test-request)"), "stream_empty", 503)
			require.True(t, session.WriteError(c, err, format))
			w := c.Writer.(interface{ Unwrap() http.ResponseWriter }).Unwrap()
			// The Gin wrapper exposes its recorder through its own Unwrap.
			recorder := w.(interface{ Unwrap() http.ResponseWriter }).Unwrap().(*httptest.ResponseRecorder)
			body := recorder.Body.String()
			assert.True(t, strings.HasPrefix(body, ": PING\n\nevent: error\ndata: "))
			assert.True(t, strings.HasSuffix(body, "\n\n"))
			var payload map[string]any
			require.NoError(t, common.UnmarshalJsonStr(strings.TrimSpace(strings.SplitN(body, "data: ", 2)[1]), &payload))
			if format == types.RelayFormatOpenAIResponses {
				assert.Equal(t, "error", payload["type"])
				assert.Equal(t, "stream_empty", payload["code"])
			} else {
				assert.Contains(t, payload, "error")
			}
			assert.Contains(t, body, "test-request")
		})
	}
	t.Run("no heartbeat keeps HTTP error status", func(t *testing.T) {
		c, _, session := recoveryTestContext(t, recoveryTestPolicy())
		err := types.NewErrorWithStatusCode(errors.New("bad request"), "invalid_request", 400)
		assert.False(t, session.WriteError(c, err, types.RelayFormatOpenAI))
		c.JSON(400, gin.H{"error": err.ToOpenAIError()})
		assert.Equal(t, "application/json; charset=utf-8", c.Writer.Header().Get("Content-Type"))
		assert.Equal(t, 400, c.Writer.Status())
	})
}

func TestStreamRecoveryPreservesLargeFirstContentAndRejectsUnboundedPrelude(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "large content", true: "oversized prelude"}[oversized], func(t *testing.T) {
			c, info, _ := recoveryTestContext(t, recoveryTestPolicy())
			data := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 100000) + "\"}\n\n"
			if oversized {
				data = strings.Repeat(": heartbeat\n", 100000)
			}
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(data))}
			err := helper.PrepareRecoverableStream(c, resp, info)
			if oversized {
				var apiErr *types.NewAPIError
				require.ErrorAs(t, err, &apiErr)
				assert.False(t, relaycommon.IsTransientStreamError(apiErr))
				assert.False(t, c.Writer.Written())
			} else {
				require.NoError(t, err)
				got, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assert.Equal(t, data, string(got))
			}
		})
	}
}

func TestStreamRecoveryCancelsUpstreamBeforeHeadersAndBeforeContent(t *testing.T) {
	service.InitHttpClient()
	for _, beforeHeaders := range []bool{true, false} {
		t.Run(map[bool]string{true: "waiting for headers", false: "metadata without content"}[beforeHeaders], func(t *testing.T) {
			cancelled := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if !beforeHeaders {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
				close(cancelled)
			}))
			defer upstream.Close()
			policy := recoveryTestPolicy()
			policy.FirstContentTimeout = 100 * time.Millisecond
			c, info, _ := recoveryTestContext(t, policy)
			req, err := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader(`{"input":"secret"}`))
			require.NoError(t, err)
			resp, err := doRequest(c, req, info)
			require.Nil(t, resp)
			var apiErr *types.NewAPIError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, types.ErrorCode("stream_first_content_timeout"), apiErr.GetErrorCode())
			assert.True(t, relaycommon.IsTransientStreamError(apiErr))
			select {
			case <-cancelled:
			case <-time.After(2 * time.Second):
				t.Fatal("first-content timeout did not cancel upstream request")
			}
			assert.False(t, info.StreamRecovery.ContentStarted())
		})
	}
}

func TestStreamRecoveryBudgetAndClientCancelStopRetry(t *testing.T) {
	service.InitHttpClient()
	for _, clientCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "total budget", true: "client cancel"}[clientCancel], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if clientCancel {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			policy := recoveryTestPolicy()
			policy.WaitBudget = 100 * time.Millisecond
			c, info, _ := recoveryTestContext(t, policy, ctx)
			req, err := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader("body"))
			require.NoError(t, err)
			_, err = doRequest(c, req, info)
			var apiErr *types.NewAPIError
			require.ErrorAs(t, err, &apiErr)
			expected := "stream_recovery_budget"
			if clientCancel {
				expected = "stream_client_cancelled"
			}
			assert.Equal(t, expected, string(apiErr.GetErrorCode()))
			assert.False(t, relaycommon.IsTransientStreamError(apiErr))
			assert.False(t, info.StreamRecovery.BeginAttempt(ctx, 31))
		})
	}
}

func TestStreamRecoveryHeartbeatKeepsOneClientConnectionDuringRetry(t *testing.T) {
	service.InitHttpClient()
	var attempts atomic.Int32
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"failed-attempt\"}}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\"}}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"success\"}\n\n")
	}))
	defer upstream.Close()
	result := make(chan map[string]any, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		info := &relaycommon.RelayInfo{IsStream: true, StartTime: time.Now(), ChannelMeta: &relaycommon.ChannelMeta{}}
		policy := recoveryTestPolicy()
		policy.HeartbeatInterval = 5 * time.Millisecond
		session := helper.StartStreamRecovery(c, info, policy)
		defer session.StopWaiting()
		for info.StreamRecovery.BeginAttempt(r.Context(), 31) {
			req, _ := http.NewRequest(http.MethodPost, upstream.URL, strings.NewReader(`{"input":"private"}`))
			resp, err := doRequest(c, req, info)
			if err == nil {
				_, _ = io.Copy(c.Writer, resp.Body)
				_ = resp.Body.Close()
				info.StreamRecovery.EndAttempt("completed", false)
				break
			}
			var apiErr *types.NewAPIError
			if !errors.As(err, &apiErr) || !relaycommon.IsTransientStreamError(apiErr) {
				break
			}
			info.StreamRecovery.EndAttempt(string(apiErr.GetErrorCode()), true)
		}
		result <- info.StreamRecovery.Snapshot()
	}))
	defer gateway.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(gateway.URL, "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	first, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, ": PING\n", first)
	close(release)
	remaining, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(remaining), "success")
	assert.NotContains(t, string(remaining), "failed-attempt")
	assert.NotContains(t, string(remaining), "server_error")
	assert.EqualValues(t, 2, attempts.Load())
	snapshot := <-result
	assert.True(t, snapshot["content_started"].(bool))
	assert.Greater(t, snapshot["heartbeats"].(int), 0)
	assert.Len(t, snapshot["attempts"], 2)
}

func TestStreamRecoveryMediaRequestsStayOutsideTextRetries(t *testing.T) {
	info := &relaycommon.RelayInfo{IsStream: true, RelayMode: relayconstant.RelayModeResponses, Request: &dto.OpenAIResponsesRequest{Tools: []byte(`[{"type":"image_generation"}]`)}}
	assert.False(t, relaycommon.StreamRecoveryEnabled(info))
	info.Request = &dto.OpenAIResponsesRequest{Tools: []byte(`[{"type":"function","name":"lookup"}]`)}
	assert.True(t, relaycommon.StreamRecoveryEnabled(info))
	info.OriginModelName = "nano-banana-pro"
	assert.False(t, relaycommon.StreamRecoveryEnabled(info))
	info.OriginModelName = "gpt-6.1-sol"
	info.RelayMode = relayconstant.RelayModeImagesGenerations
	assert.False(t, relaycommon.StreamRecoveryEnabled(info))
}
