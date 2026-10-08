package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay/channel"
	taskplugin "github.com/QuantumNous/new-api/relay/channel/task/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadImageBatchPlugin(t *testing.T) *jsplugin.LoadedPlugin {
	t.Helper()
	source, err := builtinplugins.Source("image-batch")
	require.NoError(t, err)
	plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: "image-batch"})
	require.NoError(t, err)
	return plugin
}

func TestImageBatchSubmitAndPollHooks(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	ctx := map[string]any{
		"action":        "image_generation",
		"model":         "nano-banana-pro",
		"upstreamModel": "nano-banana-pro",
		"baseUrl":       "https://upstream.example",
		"apiKey":        "upstream-key",
		"requestBody": map[string]any{
			"model": "nano-banana-pro", "prompt": "a glass tower", "n": 2, "size": "2048x1536",
		},
		"requestHeaders": map[string]string{"Idempotency-Key": "batch-test-1234"},
	}
	value, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", ctx)
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var descriptor map[string]any
	require.NoError(t, common.Unmarshal(encoded, &descriptor))
	assert.Equal(t, "https://upstream.example/v1/image-batches/generations", descriptor["url"])
	assert.Equal(t, "POST", descriptor["method"])
	assert.Equal(t, "batch-test-1234", descriptor["headers"].(map[string]any)["Idempotency-Key"])

	parsed, err := plugin.Engine.Call(t.Context(), "parseSubmitResponse", ctx, map[string]any{
		"statusCode": 202,
		"body":       map[string]any{"batch_id": "upstream-batch-1", "total": 2, "status": "queued"},
	})
	require.NoError(t, err)
	parsedJSON, err := common.Marshal(parsed)
	require.NoError(t, err)
	assert.JSONEq(t, `{"taskId":"upstream-batch-1","taskData":{"batch_id":"upstream-batch-1","total":2,"status":"queued"}}`, string(parsedJSON))

	result, err := plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{
		"publicTaskId": "task-public-1",
		"taskId":       "upstream-batch-1",
	}, map[string]any{"object": "list", "data": []any{
		map[string]any{"item_id": "item-1", "status": "succeeded", "output_url": "https://cdn.example/image.png"},
		map[string]any{"item_id": "item-2", "status": "failed", "error_message": "rejected"},
	}})
	require.NoError(t, err)
	resultJSON, err := common.Marshal(result)
	require.NoError(t, err)
	assert.JSONEq(t, `{"taskId":"task-public-1","status":"SUCCESS","reason":"rejected","url":"https://cdn.example/image.png"}`, string(resultJSON))
}

func TestImageBatchArtifactProjectsStableProxy(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	adaptor := taskplugin.New(plugin)
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "upstream-key", ChannelBaseUrl: "https://upstream.example"}})
	data, err := common.Marshal(map[string]any{"object": "list", "data": []any{
		map[string]any{"item_id": "item-1", "status": "succeeded", "output_url": "https://cdn.example/image.png"},
	}})
	require.NoError(t, err)
	task := &model.Task{TaskID: "task-public-1", Status: model.TaskStatusSuccess, Data: data}
	artifacts, err := adaptor.ListArtifacts(task)
	require.NoError(t, err)
	assert.Len(t, artifacts, 1)
	assert.Equal(t, "image", artifacts[0].Key)
	assert.Equal(t, "image", artifacts[0].Type)

	descriptor, err := adaptor.BuildContentRequest(task, "image", channel.TaskArtifactClientRequest{Method: "GET"})
	require.NoError(t, err)
	require.NotNil(t, descriptor)
	assert.Equal(t, "https://cdn.example/image.png", descriptor.URL)
	assert.True(t, descriptor.Credentialless)
}
