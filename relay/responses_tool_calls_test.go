package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesToolCallNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, input, callIDs string
		unchanged, invalid   bool
	}{
		{name: "text", input: `"hello"`, unchanged: true},
		{name: "canonical call id", input: `[{"type":"function_call_output","call_id":"call_1","output":"ok"}]`, unchanged: true, callIDs: "call_1"},
		{name: "explicit tool call alias", input: `[{"type":"function_call_output","tool_call_id":"call_1","output":"ok","custom":false}]`, callIDs: "call_1"},
		{name: "parallel calls mapped by item reference", input: `[{"type":"function_call","id":"fc_1","call_id":"call_1"},{"type":"function_call","id":"fc_2","call_id":"call_2"},{"type":"function_call_output","item_id":"fc_2","output":"two"},{"type":"function_call_output","item_id":"fc_1","output":"one"}]`, callIDs: "call_2,call_1"},
		{name: "missing id cannot be invented", input: `[{"type":"function_call_output","output":"ok"}]`, invalid: true},
		{name: "output id is not call id", input: `[{"type":"function_call_output","id":"fc_1","output":"ok"}]`, invalid: true},
		{name: "position cannot associate call", input: `[{"type":"function_call","id":"fc_1","call_id":"call_1"},{"type":"function_call_output","output":"ok"}]`, invalid: true},
		{name: "unknown item reference", input: `[{"type":"function_call_output","item_id":"fc_unknown","output":"ok"}]`, invalid: true},
		{name: "duplicate ambiguous item reference", input: `[{"type":"function_call","id":"fc_1","call_id":"call_1"},{"type":"function_call","id":"fc_1","call_id":"call_2"},{"type":"function_call_output","item_id":"fc_1","output":"ok"}]`, invalid: true},
		{name: "conflicting references", input: `[{"type":"function_call","id":"fc_1","call_id":"call_1"},{"type":"function_call_output","item_id":"fc_1","tool_call_id":"call_2","output":"ok"}]`, invalid: true},
		{name: "non-string id", input: `[{"type":"function_call_output","call_id":12,"output":"ok"}]`, invalid: true},
		{name: "blank id", input: `[{"type":"function_call_output","call_id":" ","output":"ok"}]`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"test","previous_response_id":"resp_previous","unknown":{"enabled":false},"input":` + tc.input + `}`)
			normalized, err := helper.NormalizeResponsesToolCallBody(body)
			if tc.invalid {
				require.Error(t, err)
				assert.Nil(t, normalized)
				return
			}
			require.NoError(t, err)
			if tc.unchanged {
				assert.Equal(t, body, normalized)
			}
			assert.Equal(t, "resp_previous", gjson.GetBytes(normalized, "previous_response_id").String())
			assert.Equal(t, `{"enabled":false}`, gjson.GetBytes(normalized, "unknown").Raw)
			var got []string
			for _, item := range gjson.GetBytes(normalized, "input").Array() {
				if item.Get("type").String() == "function_call_output" {
					got = append(got, item.Get("call_id").String())
				}
			}
			assert.Equal(t, tc.callIDs, strings.Join(got, ","))
		})
	}
}

func TestResponsesToolCallsOutbound(t *testing.T) {
	service.InitHttpClient()
	for _, tc := range []struct {
		name                 string
		passThrough, invalid bool
		override             map[string]any
	}{
		{name: "converted request"},
		{name: "passthrough request", passThrough: true},
		{name: "final override repaired", override: map[string]any{"operations": []any{map[string]any{"path": "input", "mode": "set", "value": []any{map[string]any{"type": "function_call_output", "tool_call_id": "call_1", "output": "ok"}}}}}},
		{name: "invalid override blocked", invalid: true, override: map[string]any{"operations": []any{map[string]any{"path": "input", "mode": "set", "value": []any{map[string]any{"type": "function_call_output", "output": "ok"}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err == nil {
					received <- body
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"error":{"message":"fixture upstream","type":"upstream_error"}}`)
			}))
			t.Cleanup(upstream.Close)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","previous_response_id":"resp_previous","input":[{"type":"function_call_output","tool_call_id":"call_1","output":"ok"}]}`))
			c.Request.Header.Set("Content-Type", "application/json")
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
			common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
			common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: tc.passThrough})
			common.SetContextKey(c, constant.ContextKeyChannelParamOverride, tc.override)
			request, err := helper.GetAndValidateResponsesRequest(c)
			require.NoError(t, err)
			assert.Equal(t, "call_1", gjson.GetBytes(request.Input, "0.call_id").String())
			info := &relaycommon.RelayInfo{Request: request, OriginModelName: "test", RelayMode: relayconstant.RelayModeResponses, RelayFormat: types.RelayFormatOpenAIResponses, RequestURLPath: "/v1/responses"}
			apiErr := ResponsesHelper(c, info)
			require.NotNil(t, apiErr)
			if tc.invalid {
				assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
				assert.True(t, types.IsSkipRetryError(apiErr))
				assert.Empty(t, received)
				return
			}
			assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
			require.Len(t, received, 1)
			body := <-received
			assert.Equal(t, "call_1", gjson.GetBytes(body, "input.0.call_id").String())
			assert.Equal(t, "resp_previous", gjson.GetBytes(body, "previous_response_id").String())
		})
	}
}
