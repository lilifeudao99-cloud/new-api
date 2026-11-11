package openai

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertImageEditRequestMultipart verifies that ConvertImageRequest
// re-serializes multipart image edit requests with all fields (including
// stream) and the file intact, both when the form was already parsed and when
// it must be re-parsed from the reusable body.
func TestConvertImageEditRequestMultipart(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newMultipartContext := func(t *testing.T, prompt string) *gin.Context {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", "gpt-image-1"))
		require.NoError(t, writer.WriteField("prompt", prompt))
		require.NoError(t, writer.WriteField("stream", "true"))
		require.NoError(t, writer.WriteField("partial_images", "3"))
		part, err := writer.CreateFormFile("image", "input.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("fake image"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		return c
	}

	convertAndReplay := func(t *testing.T, c *gin.Context, prompt string) {
		info := &relaycommon.RelayInfo{
			RelayMode: relayconstant.RelayModeImagesEdits,
		}
		request := dto.ImageRequest{
			Model:  "gpt-image-1",
			Prompt: prompt,
			Stream: common.GetPointer(true),
		}

		converted, err := (&Adaptor{}).ConvertImageRequest(c, info, request)
		require.NoError(t, err)
		convertedBody, ok := converted.(*bytes.Buffer)
		require.True(t, ok)

		replayedRequest := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(convertedBody.Bytes()))
		replayedRequest.Header.Set("Content-Type", c.Request.Header.Get("Content-Type"))
		require.NoError(t, replayedRequest.ParseMultipartForm(32<<20))

		require.Equal(t, "gpt-image-1", replayedRequest.PostForm.Get("model"))
		require.Equal(t, prompt, replayedRequest.PostForm.Get("prompt"))
		require.Equal(t, "true", replayedRequest.PostForm.Get("stream"))
		require.Equal(t, "3", replayedRequest.PostForm.Get("partial_images"))
		require.Len(t, replayedRequest.MultipartForm.File["image"], 1)

		file, err := replayedRequest.MultipartForm.File["image"][0].Open()
		require.NoError(t, err)
		defer file.Close()
		fileBytes, err := io.ReadAll(file)
		require.NoError(t, err)
		require.Equal(t, []byte("fake image"), fileBytes)
	}

	t.Run("with pre-parsed form", func(t *testing.T) {
		prompt := "edit this image"
		c := newMultipartContext(t, prompt)
		require.NoError(t, c.Request.ParseMultipartForm(32<<20))

		convertAndReplay(t, c, prompt)
	})

	t.Run("re-parses reusable body when form is missing", func(t *testing.T) {
		prompt := "edit without pre-parsed form"
		c := newMultipartContext(t, prompt)

		storage, err := common.GetBodyStorage(c)
		require.NoError(t, err)
		c.Request.Body = io.NopCloser(storage)
		c.Request.MultipartForm = nil
		c.Request.PostForm = nil

		convertAndReplay(t, c, prompt)
	})
}

func TestConvertImageEditRequestJSONSources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, fields, want, wantError string
	}{
		{
			name:   "CPA Image duplicate data URL regression",
			fields: `"images":[{"image_url":"data:image/png;base64,aGVsbG8=","url":"data:image/png;base64,aGVsbG8="}]`,
			want:   `"images":[{"image_url":"data:image/png;base64,aGVsbG8="}]`,
		},
		{
			name:   "empty sources and whitespace",
			fields: `"image":{"url":" https://example.com/image.png ","image_url":"https://example.com/image.png","file_id":" ","data":null}`,
			want:   `"image":{"image_url":"https://example.com/image.png"}`,
		},
		{
			name:   "different images remain distinct array entries",
			fields: `"images":[{"url":"https://example.com/one.png"},{"image_url":"https://example.com/two.png"}],"mask":{"url":"https://example.com/mask.png","image_url":"https://example.com/mask.png"}`,
			want:   `"images":[{"url":"https://example.com/one.png"},{"image_url":"https://example.com/two.png"}],"mask":{"image_url":"https://example.com/mask.png"}`,
		},
		{
			name:   "preserves provider metadata and explicit zero values",
			fields: `"image":{"image_url":{"url":"https://example.com/image.png","detail":"high"},"file_id":null,"metadata":{"id":9007199254740993,"url":"keep","data":"metadata"}},"output_compression":0,"stream":false`,
			want:   `"image":{"image_url":{"url":"https://example.com/image.png","detail":"high"},"metadata":{"id":9007199254740993,"url":"keep","data":"metadata"}},"output_compression":0,"stream":false`,
		},
		{
			name:      "rejects different URL sources with field locations",
			fields:    `"images":[{"url":"https://example.com/one.png","image_url":"https://example.com/two.png"}]`,
			wantError: "conflicting image sources at images[0].image_url and images[0].url",
		},
		{
			name:      "does not conflate nested URL with file ID",
			fields:    `"image":{"image_url":{"url":"https://example.com/image.png"},"file_id":"file_123"}`,
			wantError: "conflicting image sources at image.image_url and image.file_id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"gpt-image-2","prompt":"edit this image",` + tc.fields + `}`
			var request dto.ImageRequest
			require.NoError(t, common.Unmarshal([]byte(body), &request))
			before, err := common.Marshal(request)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewBufferString(body))
			c.Request.Header.Set("Content-Type", "application/json; charset=utf-8")
			converted, err := (&Adaptor{}).ConvertImageRequest(c, &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesEdits}, request)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				var apiErr *types.NewAPIError
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
				assert.True(t, types.IsSkipRetryError(apiErr))
				assert.NotContains(t, err.Error(), "https://example.com/")
			} else {
				require.NoError(t, err)
				encoded, err := common.Marshal(converted)
				require.NoError(t, err)
				assert.JSONEq(t, `{"model":"gpt-image-2","prompt":"edit this image",`+tc.want+`}`, string(encoded))
				if tc.name == "preserves provider metadata and explicit zero values" {
					assert.Contains(t, string(encoded), `"id":9007199254740993`)
				}
			}
			// Retried requests must still start from the original client DTO.
			after, err := common.Marshal(request)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after))
		})
	}
}
