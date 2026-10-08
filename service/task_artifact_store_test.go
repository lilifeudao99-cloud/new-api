package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDisabledTaskArtifactStoreHasNoStorageBehavior(t *testing.T) {
	store := GetTaskArtifactStore()
	require.NotNil(t, store)
	assert.False(t, store.Enabled())

	task := &model.Task{TaskID: "task-disabled-store"}
	ref, err := store.Resolve(task, "video")
	require.NoError(t, err)
	assert.Nil(t, ref)

	ref, err = store.Persist(t.Context(), task, types.TaskArtifact{Key: "video", Type: "video"}, strings.NewReader("content"))
	assert.Nil(t, ref)
	assert.ErrorIs(t, err, ErrTaskArtifactStoreDisabled)
	assert.ErrorIs(t, store.Serve(&gin.Context{}, task, &StoredArtifactRef{Backend: "s3"}), ErrTaskArtifactStoreDisabled)
	assert.Same(t, store, GetTaskArtifactStore())
}

func TestS3ArtifactStorePersistsAndPresignsObjects(t *testing.T) {
	var putBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			assert.Contains(t, r.Header.Get("Authorization"), "AWS4-HMAC-SHA256")
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			putBody = string(body)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	}))
	defer server.Close()
	store := &s3ArtifactStore{
		config: system_setting.TaskArtifactStoreConfig{
			S3Endpoint: server.URL, S3Bucket: "test-bucket", S3Region: "ap-southeast-1",
			S3AccessKey: "test-access", S3SecretKey: "test-secret", S3Prefix: "task/v1",
			S3PresignTTLSeconds: 300, S3InputPresignTTLSeconds: 3600,
		},
		signer: v4.NewSigner(), client: server.Client(),
	}
	inputURL, err := store.PersistInput(context.Background(), "input-1.png", "image/png", strings.NewReader("input-image"))
	require.NoError(t, err)
	assert.Equal(t, "input-image", putBody)
	assert.Contains(t, inputURL, "X-Amz-Expires=3600")
	assert.NotContains(t, inputURL, "test-secret")

	task := &model.Task{TaskID: "task-1"}
	ref, err := store.Persist(context.Background(), task, types.TaskArtifact{Key: "image-0", Type: "image", MimeType: "image/png"}, strings.NewReader("output-image"))
	require.NoError(t, err)
	assert.Equal(t, "outputs/task-1/image-0", ref.ObjectKey)
	resolved, err := store.Resolve(task, "image-0")
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, ref.ObjectKey, resolved.ObjectKey)
}

var _ TaskArtifactStore = disabledArtifactStore{}
