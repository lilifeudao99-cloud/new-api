package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// WebStaticAssetPrefix is the build output directory for content-hashed
// frontend files (JS, CSS and fonts). A file name changes whenever its
// content changes, so a URL under this prefix never refers to new content.
const WebStaticAssetPrefix = "/static/"

const immutableAssetCacheControl = "public, max-age=31536000, immutable"

// WebStaticAssets serves existing content-hashed frontend files before the
// page-navigation web rate limit. Code splitting produces many small chunks;
// counting them against GLOBAL_WEB_RATE_LIMIT made ordinary navigation (and
// users behind shared NAT addresses) receive 429 for JavaScript files. The
// assets keep their own, larger per-IP bucket and a one-year immutable cache.
//
// Missing paths and directories fall through unchanged; the web fallback
// answers them with an uncached 404 instead of the HTML shell.
func WebStaticAssets(assets static.ServeFileSystem) gin.HandlerFunc {
	fileServer := http.FileServer(assets)
	limit := GlobalWebStaticRateLimit()
	return func(c *gin.Context) {
		method := c.Request.Method
		if method != http.MethodGet && method != http.MethodHead {
			return
		}
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, WebStaticAssetPrefix) || !assets.Exists("", path) {
			return
		}
		limit(c)
		if c.IsAborted() {
			return
		}
		c.Header("Cache-Control", immutableAssetCacheControl)
		fileServer.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}
