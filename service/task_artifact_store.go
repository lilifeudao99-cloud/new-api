package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/gin-gonic/gin"
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
	signer *v4.Signer
	client *http.Client
}

var taskArtifactStore TaskArtifactStore = &disabledArtifactStore{}

func init() {
	config := system_setting.LoadTaskArtifactStoreConfig()
	if config.Mode == system_setting.TaskArtifactStoreModeS3 {
		taskArtifactStore = &s3ArtifactStore{config: config, signer: v4.NewSigner(), client: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	}
}

func GetTaskArtifactStore() TaskArtifactStore { return taskArtifactStore }

func (s *s3ArtifactStore) Enabled() bool { return true }

func (s *s3ArtifactStore) objectURL(key string) (string, error) {
	endpoint, err := url.Parse(s.config.S3Endpoint)
	if err != nil {
		return "", err
	}
	segments := []string{s.config.S3Bucket}
	if prefix := strings.Trim(s.config.S3Prefix, "/"); prefix != "" {
		segments = append(segments, prefix)
	}
	segments = append(segments, strings.TrimLeft(key, "/"))
	encoded := make([]string, 0, len(segments))
	for _, segment := range segments {
		encoded = append(encoded, url.PathEscape(segment))
	}
	base := strings.TrimRight(endpoint.Path, "/")
	baseRaw := strings.TrimRight(endpoint.EscapedPath(), "/")
	endpoint.Path = base + "/" + strings.Join(segments, "/")
	endpoint.RawPath = baseRaw + "/" + strings.Join(encoded, "/")
	return endpoint.String(), nil
}

func (s *s3ArtifactStore) put(ctx context.Context, key, mimeType string, content io.Reader) (int64, error) {
	data, err := io.ReadAll(io.LimitReader(content, maxTaskArtifactObjectBytes+1))
	if err != nil {
		return 0, err
	}
	if int64(len(data)) > maxTaskArtifactObjectBytes {
		return 0, fmt.Errorf("artifact exceeds %d byte limit", maxTaskArtifactObjectBytes)
	}
	objectURL, err := s.objectURL(key)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", mimeType)
	payloadHash := sha256.Sum256(data)
	credentials := aws.Credentials{AccessKeyID: s.config.S3AccessKey, SecretAccessKey: s.config.S3SecretKey}
	if err = s.signer.SignHTTP(ctx, credentials, req, hex.EncodeToString(payloadHash[:]), "s3", s.config.S3Region, time.Now()); err != nil {
		return 0, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("object storage PUT returned HTTP %d", resp.StatusCode)
	}
	return int64(len(data)), nil
}

func (s *s3ArtifactStore) presignGet(ctx context.Context, key string) (string, error) {
	return s.presignGetTTL(ctx, key, s.config.S3PresignTTLSeconds)
}

func (s *s3ArtifactStore) presignGetTTL(ctx context.Context, key string, ttlSeconds int) (string, error) {
	objectURL, err := s.objectURL(key)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, objectURL, nil)
	if err != nil {
		return "", err
	}
	query := req.URL.Query()
	query.Set("X-Amz-Expires", fmt.Sprint(ttlSeconds))
	req.URL.RawQuery = query.Encode()
	credentials := aws.Credentials{AccessKeyID: s.config.S3AccessKey, SecretAccessKey: s.config.S3SecretKey}
	signed, _, err := s.signer.PresignHTTP(ctx, credentials, req, "UNSIGNED-PAYLOAD", "s3", s.config.S3Region, time.Now())
	return signed, err
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
