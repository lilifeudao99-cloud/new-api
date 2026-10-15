package jsplugin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskAdaptorDoubaoReferenceVideoVerification(t *testing.T) {
	source, err := plugins.Source("doubao")
	require.NoError(t, err)
	plugin, err := pluginruntime.NewRegistry().RegisterFactory(source, pluginruntime.Options{Key: "doubao"})
	require.NoError(t, err)
	for _, upstream := range []string{"doubao-seedance-2-0-260128", "seedance-2.0"} {
		t.Run(upstream, func(t *testing.T) {
			adaptor := New(plugin)
			info := &relaycommon.RelayInfo{OriginModelName: "seedance-2.0", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: upstream, ChannelBaseUrl: "https://upstream.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor.Init(info)
			calls := map[string]int{}
			adaptor.probeReferenceVideo = func(ctx context.Context, url string) (float64, error) {
				_, hasDeadline := ctx.Deadline()
				assert.True(t, hasDeadline)
				calls[url]++
				return 2.25, nil
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			request := map[string]any{"model": "seedance-2.0", "seconds": 4, "input_seconds": 0.001,
				"metadata": map[string]any{"content": []any{
					map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://cdn.example/one.mp4"}},
					map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://cdn.example/two.mp4"}},
				}}}
			c.Set("task_request", request)
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
			for range 2 {
				facts, err := adaptor.ExtractUsageFactsValidated(c, info)
				require.NoError(t, err)
				assert.Equal(t, 4.5, facts["input_seconds"])
				assert.Equal(t, "video", facts["video_input"])
				_, err = adaptor.EstimateBillingValidated(c, info)
				require.NoError(t, err)
			}
			assert.Equal(t, map[string]int{"https://cdn.example/one.mp4": 1, "https://cdn.example/two.mp4": 1}, calls)
			adaptor.referenceVideoDurations = nil
			adaptor.probeReferenceVideo = func(context.Context, string) (float64, error) { return 0, errors.New("signed-url-secret") }
			_, err := adaptor.ExtractUsageFactsValidated(c, info)
			require.ErrorContains(t, err, "duration verification failed")
			assert.NotContains(t, err.Error(), "signed-url-secret")
		})
	}
}

func TestTaskAdaptorDoubaoVideoReferenceCannotBeSilentlyDropped(t *testing.T) {
	source, err := plugins.Source("doubao")
	require.NoError(t, err)
	plugin, err := pluginruntime.NewRegistry().RegisterFactory(source, pluginruntime.Options{Key: "doubao"})
	require.NoError(t, err)
	for _, field := range []string{"input_reference", "input_video", "video"} {
		t.Run(field, func(t *testing.T) {
			adaptor := New(plugin)
			info := &relaycommon.RelayInfo{OriginModelName: "seedance-2.0", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "doubao-seedance-2-0-260128", ChannelBaseUrl: "https://upstream.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor.Init(info)
			adaptor.probeReferenceVideo = func(context.Context, string) (float64, error) { return 3, nil }
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			c.Set("task_request", map[string]any{"model": "seedance-2.0", "seconds": 4, field: "https://cdn.example/ref.mp4"})
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
			facts, err := adaptor.ExtractUsageFactsValidated(c, info)
			require.NoError(t, err)
			assert.Equal(t, 3.0, facts["input_seconds"])
			urls, err := seedanceReferenceVideoURLs(adaptor.submit.Body.(map[string]any))
			require.NoError(t, err)
			assert.Equal(t, []string{"https://cdn.example/ref.mp4"}, urls)
		})
	}
}

func TestTaskAdaptorMappedDoubaoSecondUsage(t *testing.T) {
	source, err := plugins.Source("doubao")
	require.NoError(t, err)
	plugin, err := pluginruntime.NewRegistry().RegisterFactory(source, pluginruntime.Options{Key: "doubao"})
	require.NoError(t, err)
	for _, model := range []struct{ origin, upstream string }{
		{"seedance-2.0", "doubao-seedance-2-0-260128"},
		{"seedance-2.0-fast", "doubao-seedance-2-0-fast-260128"},
		{"seedance-2.0-mini", "doubao-seedance-2-0-mini-260615"},
		{"seedance-2.5", "doubao-seedance-2-5-260628"},
	} {
		t.Run(model.origin, func(t *testing.T) {
			adaptor := New(plugin)
			info := &relaycommon.RelayInfo{
				OriginModelName: model.origin,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: model.upstream,
					ChannelBaseUrl:    "https://upstream.example",
				},
				TaskRelayInfo: &relaycommon.TaskRelayInfo{},
			}
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			c.Set("task_request", map[string]any{"model": model.origin, "prompt": "a fox", "seconds": 4})
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
			facts, err := adaptor.ExtractUsageFactsValidated(c, info)
			require.NoError(t, err)
			assert.Equal(t, 4.0, facts["seconds"])
			assert.Equal(t, 0.0, facts["input_seconds"])
			assert.Equal(t, "720p", facts["resolution"])
			body, err := adaptor.BuildRequestBody(c, info)
			require.NoError(t, err)
			bodyBytes, err := io.ReadAll(body)
			require.NoError(t, err)
			var submitted map[string]any
			require.NoError(t, common.Unmarshal(bodyBytes, &submitted))
			assert.Equal(t, model.upstream, submitted["model"])
			assert.Equal(t, facts["resolution"], submitted["resolution"])
			assert.Equal(t, facts["seconds"], submitted["duration"])
			assert.Equal(t, model.origin, info.OriginModelName)
			assert.Equal(t, model.upstream, info.UpstreamModelName)
		})
	}
}
