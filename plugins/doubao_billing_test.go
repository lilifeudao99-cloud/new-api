package plugins_test

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadDoubaoPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("doubao")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "doubao"})
	require.NoError(t, err)
	return plugin
}

func callDoubaoHook(t *testing.T, plugin *jsplugin.LoadedPlugin, hook string, args ...any) map[string]any {
	t.Helper()
	value, err := plugin.Engine.Call(t.Context(), hook, args...)
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, common.Unmarshal(encoded, &result))
	return result
}

func TestDoubaoMappedSeedancePerSecondBilling(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	models := []struct {
		model, upstream string
		rates           map[string][2]float64
	}{
		{"seedance-2.0", "doubao-seedance-2-0-260128", map[string][2]float64{"480p": {0.35, 0.21}, "720p": {0.74, 0.45}, "1080p": {1.82, 1.11}, "4k": {3.76, 2.26}}},
		{"seedance-2.0-fast", "doubao-seedance-2-0-fast-260128", map[string][2]float64{"720p": {0.59, 0.35}}},
		{"seedance-2.0-mini", "doubao-seedance-2-0-mini-260615", map[string][2]float64{"480p": {0.17, 0.11}, "720p": {0.36, 0.22}}},
		{"seedance-2.5", "doubao-seedance-2-5-260628", map[string][2]float64{"480p": {0.51, 0.30}, "720p": {1.12, 0.68}, "1080p": {2.75, 1.65}}},
	}
	for _, model := range models {
		for resolution, rates := range model.rates {
			t.Run(model.model+"/"+resolution, func(t *testing.T) {
				request := map[string]any{"model": model.model, "prompt": "a fox", "seconds": 4, "size": resolution}
				ctx := map[string]any{"model": model.model, "upstreamModel": model.upstream, "requestBody": request, "baseUrl": "https://upstream.example", "apiKey": "test-key"}
				facts := callDoubaoHook(t, plugin, "extractUsage", ctx)
				assert.Equal(t, 4.0, facts["seconds"])
				assert.Equal(t, 0.0, facts["input_seconds"])
				assert.Equal(t, resolution, facts["resolution"])
				assert.Equal(t, "none", facts["video_input"])
				require.Contains(t, facts, "tokens")

				// Keep the existing formula shape and prices; mapped facts must
				// satisfy both the runtime and the expression-save schema checks.
				expression := fmt.Sprintf(`u("resolution") == %q ? tier(%q, u("seconds") * %g + u("input_seconds") * %g) : tier("other", 0.0)`, resolution, resolution, rates[0], rates[1])
				schema, _ := plugin.Meta.UsageForModel(model.upstream)
				require.True(t, billing_setting.TaskExprCompatible(expression, schema))
				cost, trace, err := billingexpr.RunExprWithRequest(expression, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
				require.NoError(t, err)
				assert.InDelta(t, 4*rates[0], cost, 1e-9)
				assert.Equal(t, resolution, trace.MatchedTier)

				submit := callDoubaoHook(t, plugin, "buildSubmitRequest", ctx)
				assert.Equal(t, "https://upstream.example/api/v3/contents/generations/tasks", submit["url"])
				body, ok := submit["body"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, model.upstream, body["model"])
				assert.Equal(t, 4.0, body["duration"])
				assert.Equal(t, resolution, body["resolution"])

				// Polling uses persisted identities, without the original request.
				task := map[string]any{"model": model.model, "upstreamModel": model.upstream, "taskId": "upstream-task", "baseUrl": ctx["baseUrl"]}
				query := callDoubaoHook(t, plugin, "buildQueryRequest", task)
				assert.Equal(t, "https://upstream.example/api/v3/contents/generations/tasks/upstream-task", query["url"])
				completed := map[string]any{"status": "succeeded", "duration": 6, "content": map[string]any{"video_url": "https://cdn.example/video.mp4", "resolution": resolution}, "usage": map[string]any{"completion_tokens": 999999}}
				parsed := callDoubaoHook(t, plugin, "parseTaskResult", task, completed)
				assert.Equal(t, "SUCCESS", parsed["status"])
				completionFacts := callDoubaoHook(t, plugin, "extractUsageOnComplete", task, parsed, completed)
				assert.Equal(t, 6.0, completionFacts["seconds"])
				assert.NotContains(t, completionFacts, "input_seconds", "omitted upstream usage must retain the frozen reference duration")
				snapshot := &billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), ExprVersion: 1, TaskUsageBilling: true, GroupRatio: 1, QuotaPerUnit: 500000, UsageFacts: facts}
				settled, _, err := service.EvaluateTaskCompletionUsage(snapshot, completionFacts)
				require.NoError(t, err)
				assert.Equal(t, common.QuotaRound(6*rates[0]*500000), settled.ActualQuotaAfterGroup)

				// A provider returning only tokens cannot switch a per-second
				// snapshot to token charging or discard its original duration.
				delete(completed, "duration")
				completionFacts = callDoubaoHook(t, plugin, "extractUsageOnComplete", task, parsed, completed)
				settled, _, err = service.EvaluateTaskCompletionUsage(snapshot, completionFacts)
				require.NoError(t, err)
				assert.Equal(t, common.QuotaRound(4*rates[0]*500000), settled.ActualQuotaAfterGroup)

				task["data"] = completed
				task["artifactKey"] = "video"
				task["clientRequest"] = map[string]any{"method": "GET"}
				content := callDoubaoHook(t, plugin, "buildContentRequest", task)
				assert.Equal(t, "https://cdn.example/video.mp4", content["url"])
				assert.Equal(t, true, content["credentialless"])

				request["input_reference"] = "https://cdn.example/frame.png"
				imageFacts := callDoubaoHook(t, plugin, "extractUsage", ctx)
				assert.Equal(t, 0.0, imageFacts["input_seconds"])
				imageSubmit := callDoubaoHook(t, plugin, "buildSubmitRequest", ctx)
				imageBody := imageSubmit["body"].(map[string]any)
				assert.Contains(t, imageBody["content"], map[string]any{"type": "image_url", "image_url": map[string]any{"url": request["input_reference"]}})
				delete(request, "input_reference")

				// Reference input is charged only from the host-verified duration.
				request["metadata"] = map[string]any{"resolution": resolution, "content": []any{map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://cdn.example/ref.mp4"}}}}
				request["input_seconds"] = 0.01
				ctx["inputVideoSeconds"] = 3
				facts = callDoubaoHook(t, plugin, "extractUsage", ctx)
				assert.Equal(t, 3.0, facts["input_seconds"])
				assert.Equal(t, "video", facts["video_input"])
				cost, _, err = billingexpr.RunExprWithRequest(expression, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
				require.NoError(t, err)
				assert.InDelta(t, 4*rates[0]+3*rates[1], cost, 1e-9)
				delete(ctx, "inputVideoSeconds")
				_, err = plugin.Engine.Call(t.Context(), "extractUsage", ctx)
				require.ErrorContains(t, err, "verified server-side reference duration")
			})
		}
	}
}

func TestDoubaoSDModelRoutingAndReferenceBilling(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	for _, tc := range []struct {
		model                     string
		reference, billable, rate float64
	}{
		{"sd-2.0-01", 0, 5, 0.5},
		{"sd-2.0-01", 12.2, 5, 0.5},
		{"sd-2.0-A", 0, 5, 0.6875},
		{"sd-2.0-A", 2.0001, 9, 0.55},
		{"sd-2.0-A", 7.2001, 12.201, 0.55},
		{"sd-2.5-A", 2, 9, 0.6875},
		{"sd-2.5-01", 2.1, 5, 1.0625},
		{"sd-2.5-01", 7.2, 8, 1.0625},
	} {
		t.Run(fmt.Sprintf("%s/%g", tc.model, tc.reference), func(t *testing.T) {
			request := map[string]any{"seconds": 5, "size": "720p", "prompt": "a fox", "input_seconds": 0.001}
			ctx := map[string]any{"model": tc.model, "upstreamModel": tc.model, "requestBody": request, "baseUrl": "https://xinfeng.best", "apiKey": "test-key"}
			if tc.reference > 0 {
				request["references"] = []any{map[string]any{"type": "video", "url": "https://cdn.example/reference.mp4"}}
				ctx["inputVideoSeconds"] = tc.reference
			}
			facts := callDoubaoHook(t, plugin, "extractUsage", ctx)
			assert.InDelta(t, tc.billable, facts["billable_seconds"], 1e-9)
			assert.Equal(t, tc.reference, facts["input_seconds"])
			expression := fmt.Sprintf(`tier("720p", u("billable_seconds") * %g)`, tc.rate)
			schema, _ := plugin.Meta.UsageForModel(tc.model)
			require.True(t, billing_setting.TaskExprCompatible(expression, schema))
			cost, _, err := billingexpr.RunExprWithRequest(expression, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
			require.NoError(t, err)
			assert.InDelta(t, tc.billable*tc.rate, cost, 1e-9)
			submit := callDoubaoHook(t, plugin, "buildSubmitRequest", ctx)
			assert.Equal(t, "https://xinfeng.best/v1/videos", submit["url"])
			body := submit["body"].(map[string]any)
			assert.Equal(t, tc.model, body["model"])
			assert.NotContains(t, body, "input_seconds")
			assert.Equal(t, request["references"], body["references"])
			parsed := callDoubaoHook(t, plugin, "parseSubmitResponse", ctx, map[string]any{"body": map[string]any{"id": "video-123"}})
			task := map[string]any{"model": tc.model, "state": parsed["state"], "baseUrl": ctx["baseUrl"], "taskId": "video-123"}
			query := callDoubaoHook(t, plugin, "buildQueryRequest", task)
			assert.Equal(t, "https://xinfeng.best/v1/videos/video-123", query["url"])
			completion := callDoubaoHook(t, plugin, "extractUsageOnComplete", task, map[string]any{}, map[string]any{"status": "completed"})
			assert.InDelta(t, tc.billable, completion["billable_seconds"], 1e-9, "polls retain measured reference duration when the provider omits usage")
			completion = callDoubaoHook(t, plugin, "extractUsageOnComplete", task, map[string]any{}, map[string]any{"status": "completed", "seconds": 6})
			assert.Equal(t, 6.0, completion["seconds"])
			assert.Equal(t, tc.reference, completion["input_seconds"])
			if tc.reference > 0 {
				delete(ctx, "inputVideoSeconds")
				_, err = plugin.Engine.Call(t.Context(), "extractUsage", ctx)
				require.ErrorContains(t, err, "verified server-side reference duration")
			}
		})
	}
	for _, request := range []map[string]any{
		{"seconds": 3}, {"seconds": 16}, {"seconds": 4.5},
		{"seconds": 5, "size": "4k"},
		{"seconds": 5, "size": "480p", "output": map[string]any{"resolution": "1080p"}},
		{"seconds": 5, "duration": 15},
	} {
		_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"model": "sd-2.0-A", "requestBody": request})
		require.Error(t, err, "%v", request)
	}
	_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"model": "sd-2.0-01", "requestBody": map[string]any{"seconds": 5, "size": "1080p"}})
	require.ErrorContains(t, err, "unsupported resolution")
}

func TestDoubaoMappedSeedanceRejectsUnknownOutputDuration(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	for _, seconds := range []any{nil, 0, -1, 1.5, 3601, "invalid"} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			_, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{"model": "seedance-2.0", "upstreamModel": "doubao-seedance-2-0-260128", "requestBody": map[string]any{"seconds": seconds}})
			require.ErrorContains(t, err, "explicit seconds")
		})
	}
}

func TestDoubaoNativeTokenBillingRemainsCompatible(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	ctx := map[string]any{
		"model":         "doubao-seedance-2-0-260128",
		"upstreamModel": "doubao-seedance-2-0-260128",
		"requestBody": map[string]any{
			"seconds": 4,
			"metadata": map[string]any{
				"resolution": "1080p",
				"content": []any{
					map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://cdn.example/ref.mp4"}},
				},
			},
		},
	}
	facts := callDoubaoHook(t, plugin, "extractUsage", ctx)
	assert.Equal(t, 194400.0, facts["tokens"])
	assert.NotContains(t, facts, "input_seconds")
	cost, _, err := billingexpr.RunExprWithRequest(`tier("tokens", u("tokens") * 9.8 / 1000000)`, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
	require.NoError(t, err)
	assert.InDelta(t, 1.90512, cost, 1e-9)
	ctx["usagePurpose"] = "billing_ratios"
	ratios := callDoubaoHook(t, plugin, "extractUsage", ctx)
	assert.Equal(t, map[string]any{"video_input_ratio": 31.0 / 46.0}, ratios)
}

func TestDoubaoNativeCompletionReferenceDuration(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	for _, inputSeconds := range []float64{0, 2.5, 4000} {
		facts := callDoubaoHook(t, plugin, "extractUsageOnComplete", map[string]any{"model": "seedance-2.0", "upstreamModel": "doubao-seedance-2-0-260128"}, map[string]any{}, map[string]any{"status": "succeeded", "duration": 4000, "usage": map[string]any{"input_seconds": inputSeconds}})
		assert.Equal(t, 3600.0, facts["seconds"])
		assert.Equal(t, min(inputSeconds, 3600), facts["input_seconds"])
	}
}

func TestDoubaoMappedSeedanceRejectsUnsentFileReferences(t *testing.T) {
	plugin := loadDoubaoPlugin(t)
	_, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"model":         "seedance-2.0",
		"upstreamModel": "doubao-seedance-2-0-260128",
		"requestBody":   map[string]any{"seconds": 4},
		"files":         []any{map[string]any{"field": "input_reference", "ref": "request_file:input_reference", "mimeType": "video/mp4"}},
	})
	require.ErrorContains(t, err, "reference URLs inside metadata.content")
}
