package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/require"
)

func TestDoubaoResponsesProtocol(t *testing.T) {
	testVideoResponsesProtocol(t, videoResponsesTestCase{
		pluginKey: "doubao",
		model:     "doubao-seedance-2-0-260128",
		requestBody: map[string]any{
			"model": "doubao-seedance-2-0-260128",
			"input": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "a running fox"},
				map[string]any{"type": "input_image", "image_url": "https://cdn.example/frame.png"},
			}}},
			"seconds": 6,
			"size":    "1920x1080",
		},
		wantAction: "image_to_video",
		wantRequest: map[string]any{
			"model":   "doubao-seedance-2-0-260128",
			"prompt":  "a running fox",
			"images":  []any{"https://cdn.example/frame.png"},
			"seconds": float64(6),
			"metadata": map[string]any{
				"resolution": "1080p",
			},
		},
		wantUsageKeys:  []string{"resolution", "tokens", "video_input"},
		wantVendorName: "doubao",
	})
}

func TestDoubaoSeedanceOpenAIVideoProfile(t *testing.T) {
	source, err := builtinplugins.Source("doubao")
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	plugin, err := registry.RegisterFactory(source, jsplugin.Options{Key: "doubao"})
	require.NoError(t, err)
	for _, testCase := range []struct {
		model       string
		resolutions []string
	}{
		{model: "seedance-2.0", resolutions: []string{"480p", "720p", "1080p", "4k"}},
		{model: "seedance-2.0-fast", resolutions: []string{"720p"}},
		{model: "seedance-2.0-mini", resolutions: []string{"480p", "720p"}},
		{model: "seedance-2.5", resolutions: []string{"480p", "720p", "1080p"}},
	} {
		profile, examples := plugin.Meta.UsageForModel(testCase.model)
		require.NotNil(t, profile, testCase.model)
		require.Len(t, examples, 2, testCase.model)
		require.Equal(t, testCase.resolutions, profile["resolution"].Enum, testCase.model)
	}

	decodedValue, err := plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
		"model": "seedance-2.0",
		"body": map[string]any{"kind": "json", "value": map[string]any{
			"prompt":                   "a fox running through snow",
			"seconds":                  5,
			"resolution":               "1080p",
			"input_reference":          "https://cdn.example/reference.mp4",
			"reference_video_duration": 3,
		}},
	})
	require.NoError(t, err)
	decodedBytes, err := common.Marshal(decodedValue)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(decodedBytes, &decoded))
	require.Equal(t, "image_to_video", decoded["action"])
	requestBody, ok := decoded["requestBody"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "seedance-2.0", requestBody["model"])

	usageValue, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
		"model":         "seedance-2.0",
		"upstreamModel": "seedance-2.0",
		"requestBody":   requestBody,
	})
	require.NoError(t, err)
	usageBytes, err := common.Marshal(usageValue)
	require.NoError(t, err)
	var usage map[string]any
	require.NoError(t, common.Unmarshal(usageBytes, &usage))
	require.Equal(t, float64(5), usage["seconds"])
	require.Equal(t, float64(3), usage["input_seconds"])
	require.Equal(t, "1080p", usage["resolution"])
	require.Equal(t, "video", usage["video_input"])

	imageUsageValue, err := plugin.Engine.Call(t.Context(), "extractUsage", map[string]any{
		"model":         "seedance-2.0",
		"upstreamModel": "seedance-2.0",
		"requestBody":   map[string]any{"seconds": 5, "input_reference": "https://cdn.example/reference.png"},
	})
	require.NoError(t, err)
	imageUsageBytes, err := common.Marshal(imageUsageValue)
	require.NoError(t, err)
	var imageUsage map[string]any
	require.NoError(t, common.Unmarshal(imageUsageBytes, &imageUsage))
	require.Equal(t, float64(0), imageUsage["input_seconds"])
	require.Equal(t, "none", imageUsage["video_input"])

	descriptorValue, err := plugin.Engine.Call(t.Context(), "buildSubmitRequest", map[string]any{
		"requestBody":   requestBody,
		"model":         "seedance-2.0",
		"upstreamModel": "seedance-2.0",
		"baseUrl":       "https://xinfeng.example",
		"apiKey":        "test-key",
		"files":         []any{},
	})
	require.NoError(t, err)
	descriptorBytes, err := common.Marshal(descriptorValue)
	require.NoError(t, err)
	var descriptor map[string]any
	require.NoError(t, common.Unmarshal(descriptorBytes, &descriptor))
	require.Equal(t, "https://xinfeng.example/v1/videos", descriptor["url"])
	descriptorBody, ok := descriptor["body"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "1080p", descriptorBody["size"])
	require.NotContains(t, descriptorBody, "resolution")
	require.NotContains(t, descriptorBody, "input_seconds")
	require.NotContains(t, descriptorBody, "reference_video_duration")

	artifactsValue, err := plugin.Engine.Call(t.Context(), "listArtifacts", map[string]any{
		"status": "SUCCESS",
		"data":   map[string]any{"model": "seedance-2.0", "status": "completed"},
	})
	require.NoError(t, err)
	artifactsBytes, err := common.Marshal(artifactsValue)
	require.NoError(t, err)
	var artifacts []map[string]any
	require.NoError(t, common.Unmarshal(artifactsBytes, &artifacts))
	require.Len(t, artifacts, 1)
	require.Equal(t, "video", artifacts[0]["key"])

	_, err = plugin.Engine.CallPath(t.Context(), "protocols", []string{"openai_video", "decodeRequest"}, map[string]any{
		"model": "seedance-2.0-fast",
		"body":  map[string]any{"kind": "json", "value": map[string]any{"prompt": "bad tier", "resolution": "1080p"}},
	})
	require.ErrorContains(t, err, "unsupported resolution")
}
