package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNormalizeVideoRequestRewritesSecondsBeforePluginPinning(t *testing.T) {
	router := gin.New()
	router.POST("/v1/videos", NormalizeVideoRequest(), func(c *gin.Context) {
		storage, err := common.GetBodyStorage(c)
		require.NoError(t, err)
		body, err := storage.Bytes()
		require.NoError(t, err)
		var request map[string]any
		require.NoError(t, common.Unmarshal(body, &request))
		require.Equal(t, "15", request["seconds"])
		require.Equal(t, float64(15), request["duration"])
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"sora-2","seconds":"15s"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestNormalizeVideoRequestBodyKeepsLegacySecondsType(t *testing.T) {
	normalized, changed, err := common.NormalizeVideoRequestBody([]byte(`{"model":"sora-2","seconds":" 15s "}`))
	require.NoError(t, err)
	require.True(t, changed)

	var request relaycommon.TaskSubmitReq
	require.NoError(t, common.Unmarshal(normalized, &request))
	require.Equal(t, "15", request.Seconds)
	require.Equal(t, 15, request.Duration)
}

func TestNormalizeVideoRequestBodyNormalizesGrokResolution(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		resolution string
		wantRes    string
		wantModel  string
	}{
		{name: "480p", model: "grok-imagine-video", resolution: "480p", wantRes: "480p", wantModel: "grok-imagine-video-billing-480p"},
		{name: "case insensitive", model: "grok-imagine-video", resolution: " 720P ", wantRes: "720p", wantModel: "grok-imagine-video-billing-720p"},
		{name: "invalid defaults", model: "grok-imagine-video", resolution: "4k", wantRes: "1080p", wantModel: "grok-imagine-video-billing-1080p"},
		{name: "internal alias follows resolution", model: "grok-imagine-video-billing-480p", resolution: "1080p", wantRes: "1080p", wantModel: "grok-imagine-video-billing-1080p"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			body := []byte(`{"model":"` + testCase.model + `","resolution":"` + testCase.resolution + `"}`)
			normalized, changed, err := common.NormalizeVideoRequestBody(body)
			require.NoError(t, err)
			require.True(t, changed)
			var request map[string]any
			require.NoError(t, common.Unmarshal(normalized, &request))
			require.Equal(t, testCase.wantRes, request["resolution"])
			require.Equal(t, testCase.wantModel, request["model"])
		})
	}
}

func TestNormalizeVideoRequestBodyRejectsInvalidJSON(t *testing.T) {
	_, _, err := common.NormalizeVideoRequestBody([]byte(`{"seconds":`))
	require.Error(t, err)
}

func TestNormalizeVideoRequestKeepsGrokCanonicalWhenBillingAliasIsUnavailable(t *testing.T) {
	router := gin.New()
	router.POST("/v1/videos", NormalizeVideoRequest(), func(c *gin.Context) {
		storage, err := common.GetBodyStorage(c)
		require.NoError(t, err)
		body, err := io.ReadAll(storage)
		require.NoError(t, err)
		var request map[string]any
		require.NoError(t, common.Unmarshal(body, &request))
		require.Equal(t, common.GrokVideoModel, request["model"])
		require.Equal(t, "720p", request["resolution"])
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"grok-imagine-video","resolution":" 720P "}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestNormalizeVideoRequestPassesNonJSONThrough(t *testing.T) {
	router := gin.New()
	router.POST("/v1/videos", NormalizeVideoRequest(), func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, "seconds=15s", string(body))
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader("seconds=15s"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestNormalizeVideoRequestKeepsUnchangedJSONReadable(t *testing.T) {
	router := gin.New()
	router.POST("/v1/videos", NormalizeVideoRequest(), func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, `{"model":"sora-2","seconds":15}`, string(body))
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"sora-2","seconds":15}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
}
