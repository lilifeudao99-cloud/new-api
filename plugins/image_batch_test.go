package plugins_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestImageBatchDropsUnsupportedQualityField(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"decodeGeneration"}, map[string]any{
		"model": "nano-banana-pro",
		"body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "nano-banana-pro", "prompt": "test", "quality": "high",
		}},
	})
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	requestBody := decoded["requestBody"].(map[string]any)
	assert.NotContains(t, requestBody, "quality")

	submit, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"model":          "nano-banana-pro",
		"action":         "image_generation",
		"baseUrl":        "https://upstream.example",
		"apiKey":         "upstream-key",
		"requestBody":    requestBody,
		"requestHeaders": map[string]string{"Idempotency-Key": "quality-drop-test"},
	})
	require.NoError(t, err)
	submitJSON, err := common.Marshal(submit)
	require.NoError(t, err)
	var descriptor map[string]any
	require.NoError(t, common.Unmarshal(submitJSON, &descriptor))
	assert.NotContains(t, descriptor["body"].(map[string]any), "quality")
}

func TestImageBatchDropsUnsupportedQualityFieldFromEdit(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"decodeEdit"}, map[string]any{
		"model": "nano-banana-pro",
		"body": map[string]any{
			"kind": "multipart",
			"fields": map[string]any{
				"prompt":  []any{"edit the reference"},
				"model":   []any{"nano-banana-pro"},
				"quality": []any{"high"},
			},
			"files": []any{map[string]any{
				"ref": "request_file:reference", "field": "image[]", "filename": "input.png", "mimeType": "image/png",
			}},
		},
	})
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	requestBody := decoded["requestBody"].(map[string]any)
	assert.NotContains(t, requestBody, "quality")

	submit, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"model":          "nano-banana-pro",
		"action":         "image_edit",
		"baseUrl":        "https://upstream.example",
		"apiKey":         "upstream-key",
		"requestBody":    requestBody,
		"requestHeaders": map[string]string{"Idempotency-Key": "quality-edit-test"},
	})
	require.NoError(t, err)
	submitJSON, err := common.Marshal(submit)
	require.NoError(t, err)
	var descriptor map[string]any
	require.NoError(t, common.Unmarshal(submitJSON, &descriptor))
	assert.NotContains(t, descriptor["body"].(map[string]any), "quality")
}

func TestImageBatchMultipartEditDecoderUsesHostFileReferences(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	for _, field := range []string{"image", "image[]", "image[0]", "images", "images[]", "images[1]"} {
		t.Run(field, func(t *testing.T) {
			value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"decodeEdit"}, map[string]any{
				"model": "nano-banana-pro",
				"body":  map[string]any{"kind": "multipart", "fields": map[string]any{"prompt": []any{"make it sunset"}, "model": []any{"nano-banana-pro"}}, "files": []any{map[string]any{"ref": "request_file:" + field, "field": field, "filename": "input.png", "mimeType": "image/png"}}},
			})
			require.NoError(t, err)
			encoded, err := common.Marshal(value)
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, common.Unmarshal(encoded, &decoded))
			requestBody := decoded["requestBody"].(map[string]any)
			image := requestBody["images"].([]any)[0].(map[string]any)
			assert.Equal(t, "request_file:"+field, image["__fileRef"])
			assert.Equal(t, "tos_url", image["encoding"])
		})
	}
}

func TestImageBatchResolutionAndAspectRatioNormalizeToSize(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	cases := []struct {
		name        string
		resolution  string
		aspectRatio string
		wantSize    string
	}{
		{name: "4k landscape", resolution: "4K", aspectRatio: "16:9", wantSize: "3840x2160"},
		{name: "2k 4 by 3", resolution: "2K", aspectRatio: "4:3", wantSize: "2048x1536"},
		{name: "1k square", resolution: "1K", aspectRatio: "1:1", wantSize: "1024x1024"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"decodeGeneration"}, map[string]any{
				"model": "gpt-image-2",
				"body": map[string]any{"kind": "json", "value": map[string]any{
					"model": "gpt-image-2", "prompt": "test", "resolution": tc.resolution, "aspectRatio": tc.aspectRatio,
				}},
			})
			require.NoError(t, err)
			encoded, err := common.Marshal(value)
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, common.Unmarshal(encoded, &decoded))
			requestBody := decoded["requestBody"].(map[string]any)
			assert.Equal(t, tc.wantSize, requestBody["size"])
			assert.NotContains(t, requestBody, "resolution")
			assert.NotContains(t, requestBody, "aspectRatio")
		})
	}
}

func TestImageBatchMultipartEditDecodesAspectRatioField(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"decodeEdit"}, map[string]any{
		"model": "nano-banana-2",
		"body": map[string]any{
			"kind":   "multipart",
			"fields": map[string]any{"prompt": []any{"edit"}, "model": []any{"nano-banana-2"}, "resolution": []any{"4K"}, "aspectRatio": []any{"4:3"}},
			"files":  []any{map[string]any{"ref": "request_file:image", "field": "image", "filename": "input.png", "mimeType": "image/png"}},
		},
	})
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(encoded, &decoded))
	requestBody := decoded["requestBody"].(map[string]any)
	assert.Equal(t, "4800x3584", requestBody["size"])
	assert.NotContains(t, requestBody, "resolution")
	assert.NotContains(t, requestBody, "aspectRatio")
}

func TestNanoBananaResolutionAndAspectRatioUseOfficialModelDimensions(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	cases := []struct {
		model       string
		resolution  string
		aspectRatio string
		wantSize    string
	}{
		{model: "nano-banana-2", resolution: "1K", aspectRatio: "4:3", wantSize: "1200x896"},
		{model: "nano-banana-2", resolution: "2K", aspectRatio: "4:3", wantSize: "2400x1792"},
		{model: "nano-banana-2.1", resolution: "4K", aspectRatio: "16:9", wantSize: "5504x3072"},
		{model: "nano-banana-2.1", resolution: "4K", aspectRatio: "1:8", wantSize: "1536x12288"},
		{model: "nano-banana-pro", resolution: "4K", aspectRatio: "4:3", wantSize: "4800x3584"},
		{model: "nano-banana-pro", resolution: "2K", aspectRatio: "21:9", wantSize: "3168x1344"},
	}
	for _, tc := range cases {
		t.Run(tc.model+"/"+tc.resolution+"/"+tc.aspectRatio, func(t *testing.T) {
			value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"decodeGeneration"}, map[string]any{
				"model": tc.model,
				"body": map[string]any{"kind": "json", "value": map[string]any{
					"model": tc.model, "prompt": "test", "resolution": tc.resolution, "aspectRatio": tc.aspectRatio,
				}},
			})
			require.NoError(t, err)
			encoded, err := common.Marshal(value)
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, common.Unmarshal(encoded, &decoded))
			requestBody := decoded["requestBody"].(map[string]any)
			assert.Equal(t, tc.wantSize, requestBody["size"])
			assert.NotContains(t, requestBody, "resolution")
			assert.NotContains(t, requestBody, "aspectRatio")
		})
	}
}

func TestNanoBananaProRejectsFlashOnlyAspectRatios(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	_, err := plugin.Engine.CallPath(t.Context(), "native", []string{"decodeGeneration"}, map[string]any{
		"model": "nano-banana-pro",
		"body": map[string]any{"kind": "json", "value": map[string]any{
			"model": "nano-banana-pro", "prompt": "test", "resolution": "4K", "aspectRatio": "1:8",
		}},
	})
	assert.Error(t, err)
}

func TestNanoBananaLowestAndHighestResolutionBounds(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	cases := []struct {
		model       string
		resolution  string
		aspectRatio string
		wantSize    string
	}{
		// Nano Banana 2 / 2.1 support the additional 1:8 and 8:1 extremes.
		{model: "nano-banana-2", resolution: "1K", aspectRatio: "1:8", wantSize: "384x3072"},
		{model: "nano-banana-2", resolution: "4K", aspectRatio: "8:1", wantSize: "12288x1536"},
		{model: "nano-banana-2.1", resolution: "1K", aspectRatio: "8:1", wantSize: "3072x384"},
		{model: "nano-banana-2.1", resolution: "4K", aspectRatio: "1:8", wantSize: "1536x12288"},
		// Pro's documented extremes are 9:16 and 21:9; it excludes 1:8/8:1.
		{model: "nano-banana-pro", resolution: "1K", aspectRatio: "9:16", wantSize: "768x1376"},
		{model: "nano-banana-pro", resolution: "4K", aspectRatio: "21:9", wantSize: "6336x2688"},
	}
	for _, tc := range cases {
		t.Run(tc.model+"/"+tc.resolution+"/"+tc.aspectRatio, func(t *testing.T) {
			value, err := plugin.Engine.CallPath(t.Context(), "native", []string{"decodeGeneration"}, map[string]any{
				"model": tc.model,
				"body": map[string]any{"kind": "json", "value": map[string]any{
					"model": tc.model, "prompt": "boundary validation", "resolution": tc.resolution, "aspectRatio": tc.aspectRatio,
				}},
			})
			require.NoError(t, err)
			encoded, err := common.Marshal(value)
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, common.Unmarshal(encoded, &decoded))
			requestBody := decoded["requestBody"].(map[string]any)
			assert.Equal(t, tc.wantSize, requestBody["size"])
		})
	}
}

func TestImageBatchRejectsUnsupportedResolutionAndSizeConflict(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	for _, body := range []map[string]any{
		{"model": "gpt-image-2", "prompt": "test", "resolution": "8K", "aspectRatio": "16:9"},
		{"model": "gpt-image-2", "prompt": "test", "resolution": "4K", "aspectRatio": "16:9", "size": "2048x1536"},
		{"model": "gpt-image-2.5-flare", "prompt": "test", "resolution": "2K", "aspectRatio": "4:3"},
	} {
		_, err := plugin.Engine.CallPath(t.Context(), "native", []string{"decodeGeneration"}, map[string]any{
			"model": "gpt-image-2", "body": map[string]any{"kind": "json", "value": body},
		})
		assert.Error(t, err)
	}
}

func TestImageBatchPollRequestUsesCursor(t *testing.T) {
	plugin := loadImageBatchPlugin(t)
	value, err := plugin.Engine.Call(t.Context(), "buildQueryRequest", map[string]any{"baseUrl": "https://upstream.example", "taskId": "batch-1", "cursor": "cursor+/next"})
	require.NoError(t, err)
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	var descriptor map[string]any
	require.NoError(t, common.Unmarshal(encoded, &descriptor))
	assert.Equal(t, "https://upstream.example/v1/image-batches/batch-1/items?limit=100&cursor=cursor%2B%2Fnext", descriptor["url"])
}

func TestImageBatchFetchTaskCollectsEveryCursorPage(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_, _ = io.WriteString(w, `{"object":"list","data":[{"item_id":"one","status":"running"}],"next_cursor":"next-1","has_more":true}`)
			return
		}
		if r.URL.Query().Get("cursor") != "next-1" {
			http.Error(w, "unexpected cursor", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"item_id":"two","status":"succeeded","output_url":"https://cdn.example/image.png"}],"next_cursor":"","has_more":false}`)
	}))
	defer server.Close()
	plugin := loadImageBatchPlugin(t)
	adaptor := taskplugin.New(plugin)
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "upstream-key", ChannelBaseUrl: server.URL}})
	task := &model.Task{TaskID: "public-task", PrivateData: model.TaskPrivateData{UpstreamTaskID: "batch-1"}}
	resp, err := adaptor.FetchTask(server.URL, "upstream-key", task, "")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var result struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &result))
	require.Len(t, result.Data, 2)
	assert.Equal(t, "one", result.Data[0]["item_id"])
	assert.Equal(t, "two", result.Data[1]["item_id"])
	assert.Equal(t, 2, requests)
	assert.Equal(t, int64(len(body)), resp.ContentLength)
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
	assert.Equal(t, "image-0", artifacts[0].Key)
	assert.Equal(t, "image", artifacts[0].Type)

	descriptor, err := adaptor.BuildContentRequest(task, "image-0", channel.TaskArtifactClientRequest{Method: "GET"})
	require.NoError(t, err)
	require.NotNil(t, descriptor)
	assert.Equal(t, "https://cdn.example/image.png", descriptor.URL)
	assert.True(t, descriptor.Credentialless)
}
