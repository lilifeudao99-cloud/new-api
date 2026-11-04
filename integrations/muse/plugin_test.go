package muse_test

import (
	"os"
	"testing"

	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseVideoContract(t *testing.T) {
	source, err := os.ReadFile("plugin.js")
	require.NoError(t, err)
	plugin, err := jsplugin.CompilePlugin(string(source), jsplugin.Options{})
	require.NoError(t, err)
	require.NoError(t, jsplugin.ValidateV1Meta(plugin.Meta))
	for _, model := range plugin.Meta.Models {
		t.Run(model, func(t *testing.T) {
			native, err := plugin.Engine.CallMember(t.Context(), "native", "createVideo", map[string]any{"body": map[string]any{"kind": "json", "value": map[string]any{"model": model, "prompt": "A flower", "seconds": "4"}}})
			require.NoError(t, err)
			assert.Equal(t, model, native.(map[string]any)["model"])
			intent, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": model, "body": map[string]any{"kind": "json", "value": map[string]any{"model": model, "prompt": "A flower", "seconds": 5, "use_proxy": false}}})
			require.NoError(t, err)
			body := intent.(map[string]any)["requestBody"].(map[string]any)
			assert.Equal(t, "5", body["seconds"])
			assert.Equal(t, false, body["use_proxy"])
			descriptor, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"model": model, "upstreamModel": model, "requestBody": body, "baseUrl": "https://example.com", "apiKey": "test"})
			require.NoError(t, err)
			assert.Equal(t, "https://example.com/v1/videos", descriptor.(map[string]any)["url"])
			for _, hook := range []string{"extractUsage", "extractUsageOnComplete"} {
				usage, err := plugin.Engine.Call(t.Context(), hook, map[string]any{"requestBody": map[string]any{"video_count": 100, "seconds": 60}})
				require.NoError(t, err)
				assert.EqualValues(t, 1, usage.(map[string]any)["video_count"])
			}
		})
	}
	_, err = plugin.Engine.CallMember(t.Context(), "native", "createVideo", map[string]any{"body": map[string]any{"kind": "json", "value": map[string]any{"model": "other", "prompt": "flower"}}})
	require.Error(t, err)
	created, err := plugin.Engine.CallMember(t.Context(), "native", "videoCreated", map[string]any{}, map[string]any{"task_id": "task_public", "data": map[string]any{"id": "private", "model": "muse-video"}})
	require.NoError(t, err)
	assert.Equal(t, "task_public", created.(map[string]any)["id"])
	assert.NotContains(t, created, "task_id")
	for _, seconds := range []any{0, -1, 11, 1.5, true, "NaN", 1e30} {
		_, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "muse-video", "body": map[string]any{"kind": "json", "value": map[string]any{"prompt": "flower", "seconds": seconds}}})
		require.Error(t, err)
	}
	multipart, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{"model": "muse-video", "body": map[string]any{"kind": "multipart", "fields": map[string]any{"prompt": []string{"flower"}, "seconds": []string{"5"}, "images": []string{`["https://example.com/a.png"]`}}}})
	require.NoError(t, err)
	built, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{"model": "muse-video", "requestBody": multipart.(map[string]any)["requestBody"], "baseUrl": "https://example.com", "apiKey": "test", "files": []any{map[string]any{"field": "input_reference", "ref": "opaque", "filename": "first.png"}}})
	require.NoError(t, err)
	assert.Equal(t, "multipart", built.(map[string]any)["bodyType"])
	for _, tc := range []struct{ status, want string }{{"queued", "QUEUED"}, {"in_progress", "IN_PROGRESS"}, {"completed", "SUCCESS"}, {"failed", "FAILURE"}, {"unexpected", "UNKNOWN"}} {
		result, err := plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{}, map[string]any{"status": tc.status, "data": []any{map[string]any{"url": "https://example.com/video.mp4"}}})
		require.NoError(t, err)
		assert.Equal(t, tc.want, result.(map[string]any)["status"])
		assert.Equal(t, "https://example.com/video.mp4", result.(map[string]any)["url"])
	}
	request, err := plugin.Engine.Call(t.Context(), "buildContentRequest", map[string]any{"artifactKey": "video", "upstreamTaskId": "private/id", "baseUrl": "https://example.com", "apiKey": "test", "clientRequest": map[string]any{"method": "GET"}})
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/v1/videos/private%2Fid/content", request.(map[string]any)["url"])
}
