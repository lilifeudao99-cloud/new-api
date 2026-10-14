package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// withSystemTaskRegistry swaps the package registry for the given handlers for
// the duration of a test and restores the original registry afterward.
func withSystemTaskRegistry(t *testing.T, handlers ...SystemTaskHandler) {
	t.Helper()
	systemTaskHandlersMu.Lock()
	saved := systemTaskHandlers
	systemTaskHandlers = map[string]SystemTaskHandler{}
	for _, h := range handlers {
		systemTaskHandlers[h.Type()] = h
	}
	systemTaskHandlersMu.Unlock()
	t.Cleanup(func() {
		systemTaskHandlersMu.Lock()
		systemTaskHandlers = saved
		systemTaskHandlersMu.Unlock()
	})
}

type stubScheduledHandler struct {
	taskType string
	enabled  bool
	interval time.Duration
	onRun    func(ctx context.Context, task *model.SystemTask, runnerID string)
}

type stubSystemTaskRunResult struct {
	taskID   string
	taskType string
	err      error
}

func (h *stubScheduledHandler) Type() string { return h.taskType }

func (h *stubScheduledHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	if h.onRun != nil {
		h.onRun(ctx, task, runnerID)
	}
}

func (h *stubScheduledHandler) Enabled() bool           { return h.enabled }
func (h *stubScheduledHandler) Interval() time.Duration { return h.interval }
func (h *stubScheduledHandler) NewPayload() any         { return nil }

func countSystemTasks(t *testing.T, taskType string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Where("type = ?", taskType).Count(&count).Error)
	return count
}

func TestSystemTaskSchedulerCreatesWhenDueAndDedups(t *testing.T) {
	truncate(t)

	handler := &stubScheduledHandler{taskType: "test_scheduled", enabled: true, interval: time.Minute}
	withSystemTaskRegistry(t, handler)

	runSystemTaskScheduler()
	require.Equal(t, int64(1), countSystemTasks(t, handler.taskType))

	// An active (pending) row already exists, so a second pass must not create
	// another row.
	runSystemTaskScheduler()
	require.Equal(t, int64(1), countSystemTasks(t, handler.taskType))

	// Finish the run; with a fresh updated_at the next run is not due yet.
	latest, err := model.GetLatestSystemTask(handler.taskType)
	require.NoError(t, err)
	require.NotNil(t, latest)
	_, claimed, err := model.ClaimSystemTask(latest.ID, handler.taskType, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, model.FinishSystemTask(latest.TaskID, "runner-a", model.SystemTaskStatusSucceeded, nil, ""))

	runSystemTaskScheduler()
	require.Equal(t, int64(1), countSystemTasks(t, handler.taskType))

	// Backdate the finished row beyond the interval -> the job becomes due again.
	require.NoError(t, model.DB.Model(&model.SystemTask{}).
		Where("task_id = ?", latest.TaskID).
		Update("updated_at", common.GetTimestamp()-120).Error)

	runSystemTaskScheduler()
	require.Equal(t, int64(2), countSystemTasks(t, handler.taskType))
}

func TestSystemTaskSchedulerSkipsDisabled(t *testing.T) {
	truncate(t)

	handler := &stubScheduledHandler{taskType: "test_disabled", enabled: false, interval: time.Minute}
	withSystemTaskRegistry(t, handler)

	runSystemTaskScheduler()
	assert.Equal(t, int64(0), countSystemTasks(t, handler.taskType))
}

func TestImagePollingSchedulerDoesNotWaitAnotherIntervalAfterLongRun(t *testing.T) {
	truncate(t)
	image := &stubScheduledHandler{taskType: "image_task_poll", enabled: true, interval: 5 * time.Second}
	video := &stubScheduledHandler{taskType: model.SystemTaskTypeAsyncTaskPoll, enabled: true, interval: 15 * time.Second}
	withSystemTaskRegistry(t, image, video)
	now := common.GetTimestamp()
	for _, handler := range []*stubScheduledHandler{image, video} {
		task, err := model.CreateSystemTask(handler.Type(), nil, nil)
		require.NoError(t, err)
		_, claimed, err := model.ClaimSystemTask(task.ID, handler.Type(), "poll-runner", now+60)
		require.NoError(t, err)
		require.True(t, claimed)
		// A slow pass just finished, but started more than five seconds ago.
		require.NoError(t, model.DB.Model(task).Update("created_at", now-10).Error)
		require.NoError(t, model.FinishSystemTask(task.TaskID, "poll-runner", model.SystemTaskStatusSucceeded, nil, ""))
	}
	runSystemTaskScheduler()
	assert.Equal(t, int64(2), countSystemTasks(t, image.Type()))
	assert.Equal(t, int64(1), countSystemTasks(t, video.Type()))
	// A pending/running image pass must never overlap another pass.
	runSystemTaskScheduler()
	assert.Equal(t, int64(2), countSystemTasks(t, image.Type()))
	latest, err := model.GetLatestSystemTask(image.Type())
	require.NoError(t, err)
	_, claimed, err := model.ClaimSystemTask(latest.ID, image.Type(), "poll-runner", now+60)
	require.NoError(t, err)
	require.True(t, claimed)
	runSystemTaskScheduler()
	assert.Equal(t, int64(2), countSystemTasks(t, image.Type()), "running passes must not overlap")
	require.NoError(t, model.FinishSystemTask(latest.TaskID, "poll-runner", model.SystemTaskStatusSucceeded, nil, ""))
	require.NoError(t, model.DB.Model(latest).Update("created_at", common.GetTimestamp()-3).Error)
	runSystemTaskScheduler()
	assert.Equal(t, int64(2), countSystemTasks(t, image.Type()), "short passes must still respect the interval")
	require.NoError(t, model.DB.Model(latest).Update("created_at", common.GetTimestamp()-5).Error)
	runSystemTaskScheduler()
	assert.Equal(t, int64(3), countSystemTasks(t, image.Type()), "images become due after five seconds")
}

// DSNs must point to disposable test databases, never application databases.
func TestImagePollingDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct {
		name common.DatabaseType
		env  string
	}{
		{common.DatabaseTypeSQLite, ""},
		{common.DatabaseTypeMySQL, "TEST_IMAGE_POLL_MYSQL_DSN"},
		{common.DatabaseTypePostgreSQL, "TEST_IMAGE_POLL_POSTGRES_DSN"},
	} {
		t.Run(string(dialect.name), func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open(":memory:")
			if dialect.env != "" {
				dsn := os.Getenv(dialect.env)
				if dsn == "" {
					t.Skip(dialect.env + " is not configured")
				}
				if dialect.name == common.DatabaseTypeMySQL {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			oldDB, oldLogDB := model.DB, model.LOG_DB
			oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(dialect.name, dialect.name)
			t.Cleanup(func() {
				model.DB, model.LOG_DB = oldDB, oldLogDB
				common.SetDatabaseTypes(oldMain, oldLog)
			})
			require.NoError(t, db.AutoMigrate(&model.Task{}, &model.Channel{}, &model.User{}, &model.Token{},
				&model.Log{}, &model.Midjourney{}, &model.TopUp{}, &model.UserSubscription{},
				&model.SystemTask{}, &model.SystemTaskLock{}))
			versionQuery := "select version()"
			if dialect.name == common.DatabaseTypeSQLite {
				versionQuery = "select sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database: %s", version)
			t.Run("schedule_after_long_pass", TestImagePollingSchedulerDoesNotWaitAnotherIntervalAfterLongRun)
			t.Run("dedup_and_interval", TestSystemTaskSchedulerCreatesWhenDueAndDedups)
			t.Run("scope_isolation", testImagePollingScopeIsolation)
			t.Run("upstream_image_outputs", TestImageBatchUsesUpstreamOSS)
		})
	}
}

type recordingImageArtifactStore struct {
	disabledArtifactStore
	outputs int
	inputs  int
}

func (*recordingImageArtifactStore) Enabled() bool { return true }

func (s *recordingImageArtifactStore) Persist(context.Context, *model.Task, types.TaskArtifact, io.Reader) (*StoredArtifactRef, error) {
	s.outputs++
	return nil, io.ErrUnexpectedEOF
}

func (s *recordingImageArtifactStore) PersistInput(context.Context, string, string, io.Reader) (string, error) {
	s.inputs++
	return "https://inputs.example/reference.png", nil
}

func TestImageBatchUsesUpstreamOSS(t *testing.T) {
	truncate(t)
	store := &recordingImageArtifactStore{}
	previousStore := taskArtifactStore
	taskArtifactStore = store
	t.Cleanup(func() { taskArtifactStore = previousStore })
	const userID, tokenID, channelID = 911, 911, 911
	seedUser(t, userID, 900_000)
	seedToken(t, tokenID, userID, "test-image-oss", 800_000)
	seedTaskPollingChannel(t, channelID, true)
	ch, err := model.GetChannelById(channelID, true)
	require.NoError(t, err)
	// Detect media downloads even if the result store itself was never reached.
	var downloads int
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("upstream-image"))
	}))
	t.Cleanup(media.Close)
	for _, action := range []string{"image_generation", "image_edit"} {
		task := seedPollingTask(t, channelID, "task_oss_"+action, "upstream_"+action)
		task.UserId = userID
		task.Platform = constant.TaskPlatformImageBatch
		task.Action = action
		task.Quota = 75_000
		task.PrivateData.TokenId = tokenID
		task.PrivateData.Execution = &model.TaskExecutionSnapshot{TaskPlugin: &model.TaskPluginSnapshot{Key: "image-batch"}}
		task.PrivateData.BillingContext = &model.TaskBillingContext{PerCallBilling: true, OriginModelName: "nano-banana-2"}
		require.NoError(t, model.DB.Save(task).Error)
		body, err := common.Marshal(map[string]any{"data": []any{
			map[string]any{"status": "succeeded", "output_url": media.URL + "/image.png"},
			map[string]any{"status": "failed", "error_code": "insufficient_balance", "error_message": "上游余额不足"},
		}})
		require.NoError(t, err)
		adaptor := &scriptedPollingAdaptor{body: body, parse: &relaycommon.TaskInfo{
			Status: model.TaskStatusSuccess, Progress: "100%", Url: media.URL + "/image.png",
		}}
		require.NoError(t, updateVideoSingleTask(t.Context(), adaptor, ch, task.GetUpstreamTaskID(), map[string]*model.Task{task.GetUpstreamTaskID(): task}))
		var reloaded model.Task
		require.NoError(t, model.DB.First(&reloaded, task.ID).Error)
		assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), reloaded.Status)
		assert.Equal(t, "100%", reloaded.Progress)
		assert.Equal(t, media.URL+"/image.png", reloaded.GetResultURL())
		assert.Empty(t, reloaded.PrivateData.Artifacts)
		assert.Equal(t, 75_000, reloaded.Quota)
		assert.JSONEq(t, string(body), string(reloaded.Data), "retain both upstream links and item errors")
	}
	assert.Equal(t, 0, downloads, "completion must not download result images")
	assert.Equal(t, 0, store.outputs, "completion must not upload result images")
	user, err := model.GetUserById(userID, false)
	require.NoError(t, err)
	assert.Equal(t, 900_000, user.Quota, "polling must not charge again or refund successful tasks")
	token, err := model.GetTokenById(tokenID)
	require.NoError(t, err)
	assert.Equal(t, 800_000, token.RemainQuota)
	inputURL, err := GetTaskArtifactStore().PersistInput(t.Context(), "reference.png", "image/png", strings.NewReader("reference"))
	require.NoError(t, err)
	assert.Equal(t, "https://inputs.example/reference.png", inputURL)
	assert.Equal(t, 1, store.inputs, "local reference uploads still have a usable store")
}

func TestUpstreamImageOutputsPreserveHistoricalTOSDownload(t *testing.T) {
	client, err := tos.NewClientV2("https://tos-cn-hongkong.volces.com", tos.WithRegion("cn-hongkong"), tos.WithCredentials(tos.NewStaticCredentials("test-access", "test-secret")))
	require.NoError(t, err)
	store := &s3ArtifactStore{client: client, config: system_setting.TaskArtifactStoreConfig{
		S3Bucket: "test-bucket", S3PresignTTLSeconds: 900,
	}}
	task := &model.Task{TaskID: "task_historical", PrivateData: model.TaskPrivateData{Artifacts: map[string]model.TaskArtifactReference{
		"image-0": {Backend: "s3", Bucket: "test-bucket", ObjectKey: "outputs/task_historical/image-0", MimeType: "image/png"},
	}}}
	ref, err := store.Resolve(task, "image-0")
	require.NoError(t, err)
	require.NotNil(t, ref)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/tasks/task_historical/artifacts/image-0/content", nil)
	require.NoError(t, store.Serve(c, task, ref))
	assert.Equal(t, http.StatusFound, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Location"), "outputs/task_historical/image-0")
	assert.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
}

func testImagePollingScopeIsolation(t *testing.T) {
	truncate(t)
	seedTaskPollingChannel(t, 901, true)
	video := seedPollingTask(t, 901, "poll_video", "up_video")
	image := seedPollingTask(t, 901, "poll_image", "up_image")
	require.NoError(t, model.DB.Model(image).Update("platform", constant.TaskPlatformImageBatch).Error)
	for _, status := range []model.TaskStatus{model.TaskStatusSuccess, model.TaskStatusFailure} {
		task := &model.Task{Platform: constant.TaskPlatformImageBatch, Status: status, Progress: "30%"}
		require.NoError(t, model.DB.Create(task).Error)
	}
	finished := &model.Task{Platform: constant.TaskPlatformImageBatch, Status: model.TaskStatusInProgress, Progress: "100%"}
	require.NoError(t, model.DB.Create(finished).Error)
	images := model.GetUnfinishedTasksForPolling(1, model.TaskPollingImages)
	require.Len(t, images, 1)
	assert.Equal(t, image.ID, images[0].ID, "image selection must filter before LIMIT")
	others := model.GetUnfinishedTasksForPolling(1, model.TaskPollingOther)
	require.Len(t, others, 1)
	assert.Equal(t, video.ID, others[0].ID)
	assert.True(t, model.HasUnfinishedTasksForPolling(model.TaskPollingImages))
	assert.True(t, model.HasUnfinishedTasksForPolling(model.TaskPollingOther))

	adaptor := &taskPollingFetchAdaptor{}
	oldFactory, oldTimeout, oldLimit := GetTaskAdaptorFunc, constant.TaskTimeoutMinutes, constant.TaskQueryLimit
	GetTaskAdaptorFunc = func(constant.TaskPlatform) TaskPollingAdaptor { return adaptor }
	constant.TaskTimeoutMinutes = 0
	constant.TaskQueryLimit = 100
	t.Cleanup(func() {
		GetTaskAdaptorFunc = oldFactory
		constant.TaskTimeoutMinutes, constant.TaskQueryLimit = oldTimeout, oldLimit
	})
	summary := RunTaskPollingOnceForScope(context.Background(), nil, model.TaskPollingImages)
	assert.Equal(t, 1, summary.UnfinishedTasks)
	assert.Equal(t, []string{"up_image"}, adaptor.taskIDs)
	summary = RunTaskPollingOnceForScope(context.Background(), nil, model.TaskPollingOther)
	assert.Equal(t, 1, summary.UnfinishedTasks)
	assert.Equal(t, []string{"up_image", "up_video"}, adaptor.taskIDs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunTaskPollingOnceForScope(ctx, nil, model.TaskPollingImages)
	assert.Equal(t, []string{"up_image", "up_video"}, adaptor.taskIDs)

	require.NoError(t, model.DB.Model(image).Updates(map[string]any{"status": model.TaskStatusSuccess, "progress": "100%"}).Error)
	assert.False(t, model.HasUnfinishedTasksForPolling(model.TaskPollingImages))
	assert.True(t, model.HasUnfinishedTasksForPolling(model.TaskPollingOther))
}

func TestSystemTaskClaimPassDispatchesByType(t *testing.T) {
	truncate(t)

	ran := make(chan stubSystemTaskRunResult, 1)
	handler := &stubScheduledHandler{
		taskType: "test_dispatch",
		enabled:  true,
		interval: time.Minute,
		onRun: func(_ context.Context, task *model.SystemTask, runnerID string) {
			ran <- stubSystemTaskRunResult{
				taskType: task.Type,
				err:      model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, nil, ""),
			}
		},
	}
	withSystemTaskRegistry(t, handler)

	_, err := model.CreateSystemTask(handler.taskType, nil, nil)
	require.NoError(t, err)

	runSystemTaskClaimPass("runner-dispatch")

	select {
	case got := <-ran:
		require.NoError(t, got.err)
		assert.Equal(t, handler.taskType, got.taskType)
	case <-time.After(2 * time.Second):
		t.Fatal("claimed task was not dispatched to its handler")
	}

	require.Eventually(t, func() bool {
		latest, err := model.GetLatestSystemTask(handler.taskType)
		return err == nil && latest != nil && latest.Status == model.SystemTaskStatusSucceeded
	}, 2*time.Second, 20*time.Millisecond)
}

func TestSystemTaskClaimPassDispatchesEarliestPendingByType(t *testing.T) {
	truncate(t)

	ran := make(chan stubSystemTaskRunResult, 2)
	handlerA := &stubScheduledHandler{
		taskType: "test_dispatch_a",
		enabled:  true,
		interval: time.Minute,
		onRun: func(_ context.Context, task *model.SystemTask, runnerID string) {
			ran <- stubSystemTaskRunResult{
				taskID: task.TaskID,
				err:    model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, nil, ""),
			}
		},
	}
	handlerB := &stubScheduledHandler{
		taskType: "test_dispatch_b",
		enabled:  true,
		interval: time.Minute,
		onRun: func(_ context.Context, task *model.SystemTask, runnerID string) {
			ran <- stubSystemTaskRunResult{
				taskID: task.TaskID,
				err:    model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, nil, ""),
			}
		},
	}
	withSystemTaskRegistry(t, handlerA, handlerB)

	firstA, err := model.CreateSystemTask(handlerA.taskType, nil, nil)
	require.NoError(t, err)
	secondTaskID, err := model.GenerateSystemTaskID()
	require.NoError(t, err)
	secondA := &model.SystemTask{
		TaskID: secondTaskID,
		Type:   handlerA.taskType,
		Status: model.SystemTaskStatusPending,
	}
	require.NoError(t, model.DB.Create(secondA).Error)
	firstB, err := model.CreateSystemTask(handlerB.taskType, nil, nil)
	require.NoError(t, err)

	runSystemTaskClaimPass("runner-dispatch")

	got := map[string]bool{}
	for range 2 {
		select {
		case result := <-ran:
			require.NoError(t, result.err)
			got[result.taskID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("claimed tasks were not dispatched to their handlers")
		}
	}

	assert.True(t, got[firstA.TaskID])
	assert.True(t, got[firstB.TaskID])
	assert.False(t, got[secondA.TaskID])

	require.Eventually(t, func() bool {
		reloaded, err := model.GetSystemTaskByTaskID(secondA.TaskID)
		return err == nil && reloaded != nil && reloaded.Status == model.SystemTaskStatusPending
	}, 2*time.Second, 20*time.Millisecond)
}

func TestEnqueueSystemTaskReportsCreatedAndExistingActive(t *testing.T) {
	truncate(t)

	first, created, err := EnqueueSystemTask("test_enqueue", map[string]bool{"manual": true})
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, first)

	existing, created, err := EnqueueSystemTask("test_enqueue", nil)
	require.NoError(t, err)
	require.False(t, created)
	require.NotNil(t, existing)
	assert.Equal(t, first.TaskID, existing.TaskID)

	_, claimed, err := model.ClaimSystemTask(first.ID, first.Type, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, model.FinishSystemTask(first.TaskID, "runner-a", model.SystemTaskStatusSucceeded, nil, ""))

	second, created, err := EnqueueSystemTask("test_enqueue", nil)
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, second)
	assert.NotEqual(t, first.TaskID, second.TaskID)
}
