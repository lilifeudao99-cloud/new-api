package service

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
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
	client, err := tos.NewClientV2("https://tos-cn-hongkong.volces.com", tos.WithRegion("cn-hongkong"), tos.WithCredentials(tos.NewStaticCredentials("test-access", "test-secret")))
	require.NoError(t, err)
	store := &s3ArtifactStore{
		config: system_setting.TaskArtifactStoreConfig{
			S3Endpoint: "https://tos-cn-hongkong.volces.com", S3Bucket: "test-bucket", S3Region: "cn-hongkong",
			S3AccessKey: "test-access", S3SecretKey: "test-secret", S3Prefix: "task/v1",
			S3PresignTTLSeconds: 300, S3InputPresignTTLSeconds: 3600,
		},
		client: client,
	}
	inputURL, err := store.presignGetTTL(context.Background(), "inputs/input-1.png", 3600)
	require.NoError(t, err)
	assert.Contains(t, inputURL, "X-Tos-Expires=3600")
	assert.NotContains(t, inputURL, "test-secret")

	assert.Equal(t, "task/v1/outputs/task-1/image-0", store.objectKey("outputs/task-1/image-0"))
}

var _ TaskArtifactStore = disabledArtifactStore{}
