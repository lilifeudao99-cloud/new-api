package middleware

import (
	"embed"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/webassets
var webStaticAssetsTestFS embed.FS

func useWebRateLimits(t *testing.T, pageLimit int, staticLimit int) {
	t.Helper()
	previous := []any{
		common.GlobalWebRateLimitEnable, common.GlobalWebRateLimitNum, common.GlobalWebRateLimitDuration,
		common.GlobalWebStaticRateLimitEnable, common.GlobalWebStaticRateLimitNum, common.GlobalWebStaticRateLimitDuration,
	}
	common.GlobalWebRateLimitEnable, common.GlobalWebRateLimitNum, common.GlobalWebRateLimitDuration = true, pageLimit, 180
	common.GlobalWebStaticRateLimitEnable, common.GlobalWebStaticRateLimitNum, common.GlobalWebStaticRateLimitDuration = true, staticLimit, 180
	t.Cleanup(func() {
		common.GlobalWebRateLimitEnable = previous[0].(bool)
		common.GlobalWebRateLimitNum = previous[1].(int)
		common.GlobalWebRateLimitDuration = previous[2].(int64)
		common.GlobalWebStaticRateLimitEnable = previous[3].(bool)
		common.GlobalWebStaticRateLimitNum = previous[4].(int)
		common.GlobalWebStaticRateLimitDuration = previous[5].(int64)
	})
}

func newWebStaticAssetsRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.NoRoute(
		WebStaticAssets(common.EmbedFolder(webStaticAssetsTestFS, "testdata/webassets")),
		GlobalWebRateLimit(),
		func(c *gin.Context) {
			c.String(http.StatusOK, "fallback")
		},
	)
	return router
}

func performWebStaticRequest(router http.Handler, method string, path string, remoteAddr string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = remoteAddr
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestWebStaticAssetsServeImmutableFilesOutsidePageLimit(t *testing.T) {
	useRateLimitMiniRedis(t)
	useWebRateLimits(t, 1, 100)
	router := newWebStaticAssetsRouter(t)
	remoteAddr := "192.0.2.20:1000"

	for range 10 {
		response := performWebStaticRequest(router, http.MethodGet, "/static/js/app.0123456789.js", remoteAddr)
		require.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, "public, max-age=31536000, immutable", response.Header().Get("Cache-Control"))
		assert.Contains(t, response.Header().Get("Content-Type"), "javascript")
		assert.Equal(t, "console.log(\"chunk\")\n", response.Body.String())
	}

	head := performWebStaticRequest(router, http.MethodHead, "/static/js/app.0123456789.js", remoteAddr)
	assert.Equal(t, http.StatusOK, head.Code)
	assert.Equal(t, "public, max-age=31536000, immutable", head.Header().Get("Cache-Control"))

	// Asset requests did not consume the page bucket (limit 1).
	first := performWebStaticRequest(router, http.MethodGet, "/console", remoteAddr)
	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, "fallback", first.Body.String())
	second := performWebStaticRequest(router, http.MethodGet, "/console", remoteAddr)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
}

func TestWebStaticAssetsHaveTheirOwnLimit(t *testing.T) {
	useRateLimitMiniRedis(t)
	useWebRateLimits(t, 100, 2)
	router := newWebStaticAssetsRouter(t)
	remoteAddr := "192.0.2.21:1000"

	for range 2 {
		assert.Equal(t, http.StatusOK, performWebStaticRequest(router, http.MethodGet, "/static/js/app.0123456789.js", remoteAddr).Code)
	}
	limited := performWebStaticRequest(router, http.MethodGet, "/static/js/app.0123456789.js", remoteAddr)
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.NotEmpty(t, limited.Header().Get("Retry-After"))
	assert.Empty(t, limited.Header().Get("Cache-Control"))

	other := performWebStaticRequest(router, http.MethodGet, "/static/js/app.0123456789.js", "192.0.2.22:1000")
	assert.Equal(t, http.StatusOK, other.Code)
}

func TestWebStaticAssetsLeaveOtherRequestsToFallback(t *testing.T) {
	useRateLimitMiniRedis(t)
	useWebRateLimits(t, 100, 100)
	router := newWebStaticAssetsRouter(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/static/js/missing.0123456789.js"},
		{http.MethodGet, "/static/js/"},
		{http.MethodGet, "/static/../logo.txt"},
		{http.MethodPost, "/static/js/app.0123456789.js"},
		{http.MethodGet, "/logo.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := performWebStaticRequest(router, tc.method, tc.path, "192.0.2.23:1000")
			assert.Equal(t, http.StatusOK, response.Code)
			assert.Equal(t, "fallback", response.Body.String())
			assert.NotContains(t, response.Header().Get("Cache-Control"), "immutable")
		})
	}
}
