package controller

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskRequestAuditSnapshotRedactsSecretsAndMedia(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", map[string]any{
		"model":        "grok-imagine-video",
		"prompt":       "a quiet forest",
		"resolution":   "1080p",
		"image_size":   "large",
		"duration":     6,
		"api_key":      "do-not-log",
		"input_images": []any{"data:image/png;base64,very-secret-image"},
		"callback_url": "https://example.test/callback?token=do-not-log#fragment",
	})

	snapshot := taskRequestAuditSnapshot(c)
	require.NotNil(t, snapshot)
	fields := snapshot["fields"].(map[string]any)
	assert.Equal(t, "grok-imagine-video", fields["model"])
	assert.Equal(t, "a quiet forest", fields["prompt"])
	assert.Equal(t, "1080p", fields["resolution"])
	assert.Equal(t, "large", fields["image_size"])
	assert.Equal(t, "[redacted]", fields["api_key"])
	assert.Equal(t, map[string]any{"redacted": true, "count": 1}, fields["input_images"])
	assert.Equal(t, "https://example.test/callback", fields["callback_url"])
}

func TestTaskRequestAuditSnapshotTruncatesLongPrompt(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", map[string]any{"prompt": strings.Repeat("x", maxTaskAuditStringRunes+5)})

	snapshot := taskRequestAuditSnapshot(c)
	fields := snapshot["fields"].(map[string]any)
	assert.True(t, strings.HasSuffix(fields["prompt"].(string), "…[truncated]"))
	assert.LessOrEqual(t, len([]rune(fields["prompt"].(string))), maxTaskAuditStringRunes+len([]rune("…[truncated]")))
}

func TestTaskRequestAuditSnapshotDoesNotPersistTypedImagePayload(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", relaycommon.TaskSubmitReq{
		Prompt:   "image-to-video prompt",
		Image:    "data:image/png;base64," + strings.Repeat("A", maxTaskRequestAuditBytes*2),
		Duration: 6,
	})

	snapshot := taskRequestAuditSnapshot(c)
	require.Equal(t, true, snapshot["available"])
	fields := snapshot["fields"].(map[string]any)
	assert.Equal(t, "image-to-video prompt", fields["prompt"])
	assert.Equal(t, 6, fields["duration"])
	assert.Equal(t, map[string]any{"redacted": true, "provided": true}, fields["image"])
}

func TestTaskRequestAuditSnapshotPrefersSanitizedUpstreamJSON(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", map[string]any{"prompt": "client-side prompt"})
	c.Set("upstream_task_request_body", `{"model":"grok-imagine-video-1.5","prompt":"upstream prompt","duration":6,"image_url":"https://cdn.example/image.png?signature=secret","api_key":"never-log"}`)

	snapshot := taskRequestAuditSnapshot(c)
	require.Equal(t, "upstream_request_body", snapshot["source"])
	fields := snapshot["fields"].(map[string]any)
	assert.Equal(t, "upstream prompt", fields["prompt"])
	assert.Equal(t, json.Number("6"), fields["duration"])
	assert.Equal(t, map[string]any{"redacted": true, "provided": true}, fields["image_url"])
	assert.Equal(t, "[redacted]", fields["api_key"])
}

func TestTaskRequestAuditSnapshotOmittedWhenNoTaskRequest(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	assert.Nil(t, taskRequestAuditSnapshot(c))
}
