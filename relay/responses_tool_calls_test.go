package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/openai"
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
		{name: "canonical id ignores malformed compatibility aliases", input: `[{"type":"function_call_output","call_id":"call_1","tool_call_id":42,"item_id":{},"output":"ok"}]`, unchanged: true, callIDs: "call_1"},
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

func TestResponsesToolCallIngressDiagnostics(t *testing.T) {
	input := `[{"type":"function_call","id":"private-item","call_id":"private-call","arguments":"private arguments"},{"type":"function_call_output","id":"private-output-id","call_id":null,"tool_call_id":42,"item_id":" ","output":"private tool result","private-field":"private-value"}]`
	diagnostics := helper.ResponsesToolCallInputDiagnostics([]byte(input))
	assert.Equal(t, 2, diagnostics["input_items"])
	assert.Equal(t, 1, diagnostics["function_calls"])
	assert.Equal(t, 1, diagnostics["outputs_without_call_id"])
	assert.Equal(t, []map[string]any{{"index": 1, "fields": map[string]string{"call_id": "null", "tool_call_id": "non_string", "item_id": "blank", "id": "present"}}}, diagnostics["samples"])
	encoded, err := common.Marshal(diagnostics)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private")
	longHistory := `[` + strings.TrimSuffix(strings.Repeat(`{"type":"function_call_output","output":"private"},`, 12), ",") + `]`
	diagnostics = helper.ResponsesToolCallInputDiagnostics([]byte(longHistory))
	assert.Len(t, diagnostics["samples"], 8)
	assert.Equal(t, 12, diagnostics["outputs_without_call_id"])
	assert.Equal(t, true, diagnostics["samples_truncated"])
	// The actual ingress rejection must log safe metadata, not the body.
	var logs strings.Builder
	previousWriter := gin.DefaultErrorWriter
	common.LogWriterMu.Lock()
	gin.DefaultErrorWriter = &logs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousWriter
		common.LogWriterMu.Unlock()
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(common.RequestIdKey, "diagnostic-request")
	c.Set("id", 37)
	c.Set("token_id", 19)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","previous_response_id":"private-response","input":`+input+`}`))
	c.Request.Header.Set("Content-Type", "application/json")
	request, err := helper.GetAndValidateResponsesRequest(c)
	require.Error(t, err)
	assert.Nil(t, request)
	assert.Contains(t, logs.String(), "diagnostic-request")
	assert.Contains(t, logs.String(), "user_id=37 token_id=19")
	assert.Contains(t, logs.String(), `"outputs_without_call_id":1`)
	assert.NotContains(t, logs.String(), "private")
}

func TestResponsesToolCallIDsSurviveNativeResponseRoundTrip(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	const output = `[{"type":"function_call","id":"fc_one","call_id":"call_one","name":"first_tool","arguments":"{}"},{"type":"function_call","id":"fc_two","call_id":"call_two","name":"second_tool","arguments":"{}"}]`
	const response = `{"id":"resp_roundtrip","status":"completed","output":` + output + `,"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			body := response
			contentType := "application/json"
			if stream {
				contentType = "text/event-stream"
				body = ""
				for _, item := range gjson.Parse(output).Array() {
					for _, event := range []string{"response.output_item.added", "response.output_item.done"} {
						body += `data: {"type":"` + event + `","item":` + item.Raw + "}\n\n"
					}
				}
				body += `data: {"type":"response.completed","response":` + response + "}\n\n"
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{OriginModelName: "test", DisablePing: true, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "test"}}
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{contentType}}}
			var apiErr *types.NewAPIError
			if stream {
				_, apiErr = openai.OaiResponsesStreamHandler(c, info, resp)
			} else {
				_, apiErr = openai.OaiResponsesHandler(c, info, resp)
			}
			require.Nil(t, apiErr)
			assert.Contains(t, recorder.Body.String(), `"id":"fc_one","call_id":"call_one"`)
			assert.Contains(t, recorder.Body.String(), `"id":"fc_two","call_id":"call_two"`)
			if !stream {
				assert.Equal(t, response, recorder.Body.String())
			}
			// Parallel tool outputs return in reverse order, with the original
			// call IDs, and must remain byte-for-byte intact on the next turn.
			followup := []byte(`{"model":"test","previous_response_id":"resp_roundtrip","input":[{"type":"function_call_output","call_id":"call_two","output":"second result"},{"type":"function_call_output","call_id":"call_one","output":"first result"}]}`)
			normalized, err := helper.NormalizeResponsesToolCallBody(followup)
			require.NoError(t, err)
			assert.Equal(t, followup, normalized)
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
