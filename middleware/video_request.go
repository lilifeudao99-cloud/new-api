package middleware

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// NormalizeVideoRequest folds the former video-request-normalizer sidecar
// into the host-owned OpenAI Video endpoint. It runs before plugin pinning so
// both plugin and legacy task paths receive the same canonical JSON body.
func NormalizeVideoRequest() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost || c.Request.URL.Path != "/v1/videos" ||
			!strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type"))), "application/json") {
			c.Next()
			return
		}

		storage, err := common.GetBodyStorage(c)
		if err != nil {
			status := http.StatusBadRequest
			if common.IsRequestBodyTooLargeError(err) {
				status = http.StatusRequestEntityTooLarge
			}
			abortWithOpenAiMessage(c, status, "invalid JSON request body")
			return
		}
		raw, err := storage.Bytes()
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "invalid JSON request body")
			return
		}

		var request map[string]any
		if err := common.Unmarshal(raw, &request); err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "invalid JSON request body")
			return
		}
		changed := common.NormalizeVideoRequestMap(request)
		if !changed {
			// GetBodyStorage consumes and closes the original request body. Keep
			// the standard request reader usable for downstream legacy handlers.
			if _, err := storage.Seek(0, io.SeekStart); err != nil {
				abortWithOpenAiMessage(c, http.StatusBadRequest, "invalid JSON request body")
				return
			}
			c.Request.Body = io.NopCloser(storage)
			c.Next()
			return
		}

		normalized, err := common.Marshal(request)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, "invalid JSON request body")
			return
		}
		newStorage, err := common.CreateBodyStorage(normalized)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusInternalServerError, "failed to normalize video request")
			return
		}
		_ = storage.Close()
		c.Set(common.KeyBodyStorage, newStorage)
		c.Set(common.KeyRequestBody, nil)
		c.Request.Body = io.NopCloser(newStorage)
		c.Request.ContentLength = int64(len(normalized))
		c.Request.Header.Set("Content-Length", strconv.Itoa(len(normalized)))
		c.Next()
	}
}
