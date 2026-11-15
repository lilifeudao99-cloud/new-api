package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldRetryHonorsPinRetryMode(t *testing.T) {
	openaiErr := types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError)

	c := newPinRetryContext()
	assert.True(t, shouldRetry(c, openaiErr, 1))

	origin := newPinRetryContext()
	service.GetChannelConstraints(origin).AddPin(dto.ChannelPin{
		ChannelId: 2,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	assert.True(t, shouldRetry(origin, openaiErr, 1), "origin pin retries on the same channel")

	token := newPinRetryContext()
	service.GetChannelConstraints(token).AddPin(dto.ChannelPin{
		ChannelId: 1,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	assert.False(t, shouldRetry(token, openaiErr, 1), "token pin suppresses retry")
}

func TestStreamRecoveryRetryHonorsOutputCancellationPinsAndErrorClass(t *testing.T) {
	tests := []struct {
		name                string
		status              int
		code, message       string
		output, cancel, pin bool
		want                bool
	}{
		{name: "transient 503", status: 503, want: true},
		{name: "transient 429", status: 429, want: true},
		{name: "first content timeout", status: 504, code: "stream_first_content_timeout", want: true},
		{name: "parameters", status: 400, want: false},
		{name: "balance", status: 429, code: "insufficient_quota", want: false},
		{name: "balance message", status: 429, message: "余额不足", want: false},
		{name: "policy", status: 503, code: "content_policy_violation", want: false},
		{name: "output already begun", status: 503, output: true, want: false},
		{name: "client gone", status: 503, cancel: true, want: false},
		{name: "single attempt pin", status: 503, pin: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newPinRetryContext()
			ctx, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			info := &relaycommon.RelayInfo{IsStream: true, StartTime: time.Now()}
			session := helper.StartStreamRecovery(c, info, relaycommon.DefaultStreamRecoveryPolicy())
			defer session.StopWaiting()
			require.True(t, info.StreamRecovery.BeginAttempt(ctx, 1))
			defer info.StreamRecovery.EndAttempt("test", false)
			if tt.output {
				require.True(t, info.StreamRecovery.AcceptContent())
			}
			if tt.cancel {
				cancel()
			}
			if tt.pin {
				service.GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: 1, Source: dto.PinSourceToken, RetryMode: dto.PinRetrySingleAttempt})
			}
			err := types.NewErrorWithStatusCode(errors.New(tt.message), types.ErrorCode(tt.code), tt.status)
			assert.Equal(t, tt.want, shouldRetry(c, err, 2))
		})
	}
}

func TestShouldRetryTaskRelayHonorsPinRetryMode(t *testing.T) {
	taskErr := &dto.TaskError{StatusCode: http.StatusInternalServerError}

	c := newPinRetryContext()
	assert.True(t, shouldRetryTaskRelay(c, 1, taskErr, 1))

	origin := newPinRetryContext()
	service.GetChannelConstraints(origin).AddPin(dto.ChannelPin{
		ChannelId: 2,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	assert.True(t, shouldRetryTaskRelay(origin, 2, taskErr, 1))

	token := newPinRetryContext()
	service.GetChannelConstraints(token).AddPin(dto.ChannelPin{
		ChannelId: 1,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	assert.False(t, shouldRetryTaskRelay(token, 1, taskErr, 1))
}

func TestSameChannelPinsMergeToStricterRetryMode(t *testing.T) {
	c := newPinRetryContext()
	constraints := service.GetChannelConstraints(c)
	constraints.AddPin(dto.ChannelPin{
		ChannelId: 7,
		Source:    dto.PinSourceOriginTask,
		Rank:      dto.PinRankOriginTask,
		RetryMode: dto.PinRetrySameChannel,
	})
	constraints.AddPin(dto.ChannelPin{
		ChannelId: 7,
		Source:    dto.PinSourceToken,
		Rank:      dto.PinRankToken,
		RetryMode: dto.PinRetrySingleAttempt,
	})
	pin, found, overridden := constraints.ResolvedPin()
	require.True(t, found)
	assert.Equal(t, 7, pin.ChannelId)
	assert.Equal(t, dto.PinRetrySingleAttempt, pin.RetryMode)
	assert.Empty(t, overridden)
	assert.False(t, shouldRetry(c, types.NewOpenAIError(errors.New("upstream"), types.ErrorCodeBadResponseStatusCode, http.StatusInternalServerError), 1))
}

func newPinRetryContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}
