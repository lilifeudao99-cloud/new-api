package common

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

var (
	ErrFirstContentTimeout  = errors.New("upstream first content timeout")
	ErrStreamRecoveryBudget = errors.New("stream recovery wait budget exhausted")
	ErrStreamClientWrite    = errors.New("client stream write failed")
)

// StreamRecoveryPolicy bounds only the wait BEFORE usable output. Once output
// or a tool invocation is observed, the normal streaming timeout takes over.
type StreamRecoveryPolicy struct {
	FirstContentTimeout time.Duration
	WaitBudget          time.Duration
	MaxAttempts         int
	HeartbeatInterval   time.Duration
}

func DefaultStreamRecoveryPolicy() StreamRecoveryPolicy {
	return StreamRecoveryPolicy{
		FirstContentTimeout: time.Duration(max(1, common.GetEnvOrDefault("STREAM_FIRST_CONTENT_TIMEOUT_SECONDS", 75))) * time.Second,
		WaitBudget:          time.Duration(max(1, common.GetEnvOrDefault("STREAM_RECOVERY_BUDGET_SECONDS", 120))) * time.Second,
		MaxAttempts:         min(5, max(1, common.GetEnvOrDefault("STREAM_RECOVERY_MAX_ATTEMPTS", 3))),
		HeartbeatInterval:   10 * time.Second,
	}
}

// StreamRecoveryEnabled excludes media and hosted tools: replaying those can
// create a second paid job even before they produce their first output event.
func StreamRecoveryEnabled(info *RelayInfo) bool {
	if info == nil || !info.IsStream || info.IsChannelTest || !common.GetEnvOrDefaultBool("STREAM_RECOVERY_ENABLED", true) {
		return false
	}
	model := strings.ToLower(info.OriginModelName)
	if strings.Contains(model, "image") || strings.Contains(model, "banana") || strings.Contains(model, "video") || strings.HasPrefix(model, "sora") || strings.HasPrefix(model, "veo") || strings.HasPrefix(model, "seedance") || strings.HasPrefix(model, "dall-e") {
		return false
	}
	switch info.RelayMode {
	case relayconstant.RelayModeChatCompletions, relayconstant.RelayModeCompletions, relayconstant.RelayModeResponses:
	default:
		if info.RelayFormat != types.RelayFormatClaude {
			return false
		}
	}
	switch req := info.Request.(type) {
	case *dto.OpenAIResponsesRequest:
		var tools []struct {
			Type string `json:"type"`
		}
		if len(req.Tools) > 0 {
			if common.Unmarshal(req.Tools, &tools) != nil {
				return false
			}
			for _, tool := range tools {
				if tool.Type != "function" && tool.Type != "custom" {
					return false
				}
			}
		}
	case *dto.GeneralOpenAIRequest:
		// Audio and image outputs need their own task recovery semantics.
		if len(req.Audio) > 0 || strings.Contains(string(req.Modalities), "audio") || strings.Contains(string(req.Modalities), "image") {
			return false
		}
		for _, tool := range req.Tools {
			if tool.Type != "function" {
				return false
			}
		}
	case *dto.ClaudeRequest:
		// Normal Claude tools run on the client. Server tools (web search, MCP,
		// etc.) can already be executing before their first visible event.
		if req.Tools != nil {
			data, err := common.Marshal(req.Tools)
			if err != nil {
				return false
			}
			var tools []struct {
				Type string `json:"type"`
			}
			if common.Unmarshal(data, &tools) != nil {
				return false
			}
			for _, tool := range tools {
				if tool.Type != "" && tool.Type != "custom" {
					return false
				}
			}
		}
	default:
		return false
	}
	return true
}

// These adaptors all use channel.DoApiRequest for the outbound stream. SDK or
// proprietary protocols must opt in only after they implement the same gate.
func StreamRecoverySupportsChannel(channelType int) bool {
	apiType, _ := common.ChannelType2APIType(channelType)
	return apiType == constant.APITypeOpenAI || apiType == constant.APITypeAnthropic || apiType == constant.APITypeCodex || apiType == constant.APITypeSub2API || apiType == constant.APITypeNewAPI
}

// StreamAttempt contains timings and classifications only. Never put prompts,
// response text, credentials, URLs, headers or upstream error bodies here.
type StreamAttempt struct {
	Number         int    `json:"attempt"`
	ChannelID      int    `json:"channel_id"`
	StartedMS      int64  `json:"started_ms"`
	RequestSentMS  int64  `json:"request_sent_ms,omitempty"`
	FirstByteMS    int64  `json:"first_byte_ms,omitempty"`
	HeadersMS      int64  `json:"headers_ms,omitempty"`
	FirstEventMS   int64  `json:"first_event_ms,omitempty"`
	FirstContentMS int64  `json:"first_content_ms,omitempty"`
	EndedMS        int64  `json:"ended_ms,omitempty"`
	DNSMS          int64  `json:"dns_ms,omitempty"`
	ConnectMS      int64  `json:"connect_ms,omitempty"`
	TLSMS          int64  `json:"tls_ms,omitempty"`
	StatusCode     int    `json:"status_code,omitempty"`
	Protocol       string `json:"protocol,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	Retried        bool   `json:"retried,omitempty"`
	RequestWrites  int    `json:"request_writes,omitempty"`
	TerminalEvent  string `json:"terminal_event,omitempty"`
	TerminalMS     int64  `json:"terminal_ms,omitempty"`
}

type StreamRecovery struct {
	mu                sync.Mutex
	Policy            StreamRecoveryPolicy
	started           time.Time
	waitStarted       time.Time
	attempts          []StreamAttempt
	ctx               context.Context
	cancel            context.CancelCauseFunc
	timer             *time.Timer
	content           bool
	heartbeats        int
	clientCancelledMS int64
}

func NewStreamRecovery(started time.Time, policy StreamRecoveryPolicy) *StreamRecovery {
	if started.IsZero() {
		started = time.Now()
	}
	return &StreamRecovery{Policy: policy, started: started, waitStarted: time.Now()}
}

func (s *StreamRecovery) BeginAttempt(parent context.Context, channelID int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := s.Policy.WaitBudget - time.Since(s.waitStarted)
	if s.content || len(s.attempts) >= s.Policy.MaxAttempts || remaining <= 0 || parent.Err() != nil {
		return false
	}
	s.ctx, s.cancel = context.WithCancelCause(parent)
	deadline, cause := s.Policy.FirstContentTimeout, ErrFirstContentTimeout
	if remaining < deadline {
		deadline, cause = remaining, ErrStreamRecoveryBudget
	}
	cancel := s.cancel
	s.timer = time.AfterFunc(deadline, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.content {
			cancel(cause)
		}
	})
	s.attempts = append(s.attempts, StreamAttempt{Number: len(s.attempts) + 1, ChannelID: channelID, StartedMS: time.Since(s.started).Milliseconds()})
	return true
}

func (s *StreamRecovery) Context() context.Context { s.mu.Lock(); defer s.mu.Unlock(); return s.ctx }

func (s *StreamRecovery) Observe(event string, status int, protocol string, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attempts) == 0 {
		return
	}
	a := &s.attempts[len(s.attempts)-1]
	ms := max(1, time.Since(s.started).Milliseconds())
	switch event {
	case "request_sent":
		a.RequestSentMS = ms
		a.RequestWrites++
	case "first_byte":
		a.FirstByteMS = ms
	case "headers":
		a.HeadersMS = ms
		a.StatusCode = status
		a.Protocol = protocol
	case "event":
		if a.FirstEventMS == 0 {
			a.FirstEventMS = ms
		}
	case "dns":
		a.DNSMS += duration.Milliseconds()
	case "connect":
		a.ConnectMS += duration.Milliseconds()
	case "tls":
		a.TLSMS += duration.Milliseconds()
	case "heartbeat":
		s.heartbeats++
	case "client_cancelled":
		if s.clientCancelledMS == 0 {
			s.clientCancelledMS = ms
		}
	case "completed", "upstream_failed", "upstream_incomplete", "upstream_cancelled":
		a.TerminalEvent = event
		a.TerminalMS = ms
	}
}

func (s *StreamRecovery) ContentStarted() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.content }

func (s *StreamRecovery) AbortWaiting(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel(cause)
	}
}

func (s *StreamRecovery) AcceptContent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil || s.ctx.Err() != nil {
		return false
	}
	s.content = true
	if s.timer != nil {
		s.timer.Stop()
	}
	if len(s.attempts) > 0 {
		s.attempts[len(s.attempts)-1].FirstContentMS = max(1, time.Since(s.started).Milliseconds())
	}
	return true
}

func (s *StreamRecovery) CanRetry() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.content && len(s.attempts) < s.Policy.MaxAttempts && time.Since(s.waitStarted) < s.Policy.WaitBudget && (s.ctx == nil || context.Cause(s.ctx) != ErrStreamRecoveryBudget)
}

func (s *StreamRecovery) EndAttempt(outcome string, retried bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.cancel != nil {
		s.cancel(context.Canceled)
	}
	if len(s.attempts) > 0 {
		a := &s.attempts[len(s.attempts)-1]
		a.EndedMS = time.Since(s.started).Milliseconds()
		a.Outcome = outcome
		a.Retried = retried
	}
}

func (s *StreamRecovery) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{"attempts": append([]StreamAttempt(nil), s.attempts...), "elapsed_ms": time.Since(s.started).Milliseconds(), "content_started": s.content, "heartbeats": s.heartbeats, "client_cancelled_ms": s.clientCancelledMS}
}

// IsTransientStreamError is intentionally narrower than legacy relay retries.
// A 429 for an exhausted wallet or content restriction must never be replayed.
func IsTransientStreamError(err *types.NewAPIError) bool {
	if err == nil || types.IsSkipRetryError(err) {
		return false
	}
	text := strings.ToLower(string(err.GetErrorCode()) + " " + err.Error())
	for _, blocked := range []string{"insufficient", "余额不足", "额度不足", "quota_exceeded", "billing_hard_limit", "credit balance", "payment_required", "content_policy", "content_filter", "safety", "sensitive", "violation", "invalid_request", "permission_denied", "authentication"} {
		if strings.Contains(text, blocked) {
			return false
		}
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeDoRequestFailed, "stream_first_content_timeout", "stream_empty", "stream_connection_interrupted":
		return true
	}
	switch err.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// Keep the diagnostic vocabulary fixed even if an upstream puts arbitrary
// content in its error.code field. The original error log stays separate.
func StreamRecoveryErrorOutcome(err *types.NewAPIError) string {
	switch err.GetErrorCode() {
	case "stream_first_content_timeout", "stream_recovery_budget", "stream_client_cancelled", "stream_downstream_write_failed", "stream_empty", "stream_connection_interrupted", "stream_transport_timeout", "stream_request_rejected", "stream_prelude_limit", types.ErrorCodeBadResponseBody:
		return string(err.GetErrorCode())
	}
	if !IsTransientStreamError(err) {
		return "request_rejected"
	}
	if err.StatusCode == http.StatusTooManyRequests {
		return "upstream_rate_limited"
	}
	return "upstream_unavailable"
}
