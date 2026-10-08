package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/enum"
)

// StoredArtifactRef describes a persisted artifact object. Object keys remain
// private task data and are never included in public task DTOs.
type StoredArtifactRef struct {
	Backend   string
	Bucket    string
	ObjectKey string
	MimeType  string
	Size      int64
}

// TaskArtifactStore is the persistence boundary for generated artifacts and
// user-uploaded image inputs.
type TaskArtifactStore interface {
	Enabled() bool
	Resolve(task *model.Task, artifactKey string) (*StoredArtifactRef, error)
	Persist(ctx context.Context, task *model.Task, artifact types.TaskArtifact, content io.Reader) (*StoredArtifactRef, error)
	PersistInput(ctx context.Context, objectName, mimeType string, content io.Reader) (string, error)
	Serve(c *gin.Context, task *model.Task, ref *StoredArtifactRef) error
}

var ErrTaskArtifactStoreDisabled = errors.New("task artifact store is disabled")

type disabledArtifactStore struct{}

func (disabledArtifactStore) Enabled() bool { return false }
func (disabledArtifactStore) Resolve(*model.Task, string) (*StoredArtifactRef, error) {
	return nil, nil
}
func (disabledArtifactStore) Persist(context.Context, *model.Task, types.TaskArtifact, io.Reader) (*StoredArtifactRef, error) {
	return nil, ErrTaskArtifactStoreDisabled
}
func (disabledArtifactStore) PersistInput(context.Context, string, string, io.Reader) (string, error) {
	return "", ErrTaskArtifactStoreDisabled
}
func (disabledArtifactStore) Serve(*gin.Context, *model.Task, *StoredArtifactRef) error {
	return ErrTaskArtifactStoreDisabled
}

type s3ArtifactStore struct {
	config system_setting.TaskArtifactStoreConfig
	client *tos.ClientV2
}

var taskArtifactStore TaskArtifactStore = &disabledArtifactStore{}

func init() {
	config := system_setting.LoadTaskArtifactStoreConfig()
	if config.Mode == system_setting.TaskArtifactStoreModeS3 {
		client, err := tos.NewClientV2(config.S3Endpoint, tos.WithRegion(config.S3Region), tos.WithCredentials(tos.NewStaticCredentials(config.S3AccessKey, config.S3SecretKey)))
		if err == nil {
			taskArtifactStore = &s3ArtifactStore{config: config, client: client}
		}
	}
}

func GetTaskArtifactStore() TaskArtifactStore { return taskArtifactStore }

func (s *s3ArtifactStore) Enabled() bool { return true }

func (s *s3ArtifactStore) put(ctx context.Context, key, mimeType string, content io.Reader) (int64, error) {
	data, err := io.ReadAll(io.LimitReader(content, maxTaskArtifactObjectBytes+1))
	if err != nil {
		return 0, err
	}
	if int64(len(data)) > maxTaskArtifactObjectBytes {
		return 0, fmt.Errorf("artifact exceeds %d byte limit", maxTaskArtifactObjectBytes)
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	_, err = s.client.PutObjectV2(ctx, &tos.PutObjectV2Input{PutObjectBasicInput: tos.PutObjectBasicInput{Bucket: s.config.S3Bucket, Key: s.objectKey(key), ContentLength: int64(len(data)), ContentType: mimeType}, Content: strings.NewReader(string(data))})
	if err != nil {
		return 0, fmt.Errorf("object storage PUT failed: %w", err)
	}
	return int64(len(data)), nil
}

func (s *s3ArtifactStore) presignGet(ctx context.Context, key string) (string, error) {
	return s.presignGetTTL(ctx, key, s.config.S3PresignTTLSeconds)
}

func (s *s3ArtifactStore) presignGetTTL(ctx context.Context, key string, ttlSeconds int) (string, error) {
	result, err := s.client.PreSignedURL(&tos.PreSignedURLInput{HTTPMethod: enum.HttpMethodGet, Bucket: s.config.S3Bucket, Key: s.objectKey(key), Expires: int64(ttlSeconds)})
	if err != nil {
		return "", err
	}
	return result.SignedUrl, nil
}

func (s *s3ArtifactStore) objectKey(key string) string {
	prefix := strings.Trim(s.config.S3Prefix, "/")
	if prefix == "" {
		return strings.TrimLeft(key, "/")
	}
	return prefix + "/" + strings.TrimLeft(key, "/")
}

func (s *s3ArtifactStore) Persist(ctx context.Context, task *model.Task, artifact types.TaskArtifact, content io.Reader) (*StoredArtifactRef, error) {
	if task == nil || task.TaskID == "" || artifact.Key == "" {
		return nil, errors.New("task and artifact key are required")
	}
	key := "outputs/" + task.TaskID + "/" + artifact.Key
	size, err := s.put(ctx, key, artifact.MimeType, content)
	if err != nil {
		return nil, err
	}
	ref := &StoredArtifactRef{Backend: "s3", Bucket: s.config.S3Bucket, ObjectKey: key, MimeType: artifact.MimeType, Size: size}
	if task.PrivateData.Artifacts == nil {
		task.PrivateData.Artifacts = make(map[string]model.TaskArtifactReference)
	}
	task.PrivateData.Artifacts[artifact.Key] = model.TaskArtifactReference{Backend: ref.Backend, Bucket: ref.Bucket, ObjectKey: ref.ObjectKey, MimeType: ref.MimeType, Size: ref.Size}
	return ref, nil
}

func (s *s3ArtifactStore) PersistInput(ctx context.Context, objectName, mimeType string, content io.Reader) (string, error) {
	if objectName == "" {
		return "", errors.New("input object name is required")
	}
	key := "inputs/" + objectName
	if _, err := s.put(ctx, key, mimeType, content); err != nil {
		return "", err
	}
	return s.presignGetTTL(ctx, key, s.config.S3InputPresignTTLSeconds)
}

func (s *s3ArtifactStore) Resolve(task *model.Task, artifactKey string) (*StoredArtifactRef, error) {
	if task == nil {
		return nil, nil
	}
	ref, ok := task.PrivateData.Artifacts[artifactKey]
	if !ok || ref.Backend != "s3" || ref.Bucket != s.config.S3Bucket || ref.ObjectKey == "" {
		return nil, nil
	}
	return &StoredArtifactRef{Backend: ref.Backend, Bucket: ref.Bucket, ObjectKey: ref.ObjectKey, MimeType: ref.MimeType, Size: ref.Size}, nil
}

func (s *s3ArtifactStore) Serve(c *gin.Context, _ *model.Task, ref *StoredArtifactRef) error {
	if ref == nil || ref.Backend != "s3" || ref.Bucket != s.config.S3Bucket {
		return errors.New("invalid stored artifact reference")
	}
	url, err := s.presignGet(c.Request.Context(), ref.ObjectKey)
	if err != nil {
		return err
	}
	c.Header("Cache-Control", "private, no-store")
	c.Redirect(http.StatusFound, url)
	return nil
}

const maxTaskArtifactObjectBytes = 128 << 20
