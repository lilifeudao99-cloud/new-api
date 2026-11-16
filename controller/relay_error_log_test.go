package controller

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStreamRecoveryRelayFailoverDoesNotSettleEmptyAttempt(t *testing.T) {
	// The complete relay uses process-global channel caches and background
	// workers. Isolate its fixture from other controller tests and their workers.
	if os.Getenv("NEW_API_STREAM_RECOVERY_TEST_PROCESS") != "1" {
		for _, scenario := range []string{"empty", "timeout", "third-channel", "exhausted"} {
			t.Run(scenario, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestStreamRecoveryRelayFailoverDoesNotSettleEmptyAttempt$", "-test.timeout=30s")
				cmd.Env = append(os.Environ(), "NEW_API_STREAM_RECOVERY_TEST_PROCESS=1", "NEW_API_STREAM_RECOVERY_SCENARIO="+scenario)
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", output)
			})
		}
		return
	}
	scenario := os.Getenv("NEW_API_STREAM_RECOVERY_SCENARIO")
	t.Setenv("STREAM_RECOVERY_ENABLED", "true")
	t.Setenv("STREAM_FIRST_CONTENT_TIMEOUT_SECONDS", "1")
	t.Setenv("STREAM_RECOVERY_BUDGET_SECONDS", "5")
	t.Setenv("STREAM_RECOVERY_MAX_ATTEMPTS", "3")
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousMemory, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	previousCount, previousErrors, previousConsume := constant.CountToken, constant.ErrorLogEnabled, common.LogConsumeEnabled
	previousRatios := ratio_setting.ModelRatio2JSONString()
	previousFreePreConsume := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	previousBatch := common.BatchUpdateEnabled
	previousStreamingTimeout := constant.StreamingTimeout
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}, &model.Log{}, &model.Token{}))
	model.DB, model.LOG_DB = database, database
	common.MemoryCacheEnabled, common.RedisEnabled = true, false
	constant.CountToken, constant.ErrorLogEnabled, common.LogConsumeEnabled = false, true, true
	common.BatchUpdateEnabled = false
	constant.StreamingTimeout = 5
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"recovery-integration":0}`))
	t.Cleanup(func() {
		// Settlement dispatches performance sampling asynchronously. Join its
		// lifetime before restoring the database and cache globals it reads.
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 3*time.Second, time.Millisecond)
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.MemoryCacheEnabled, common.RedisEnabled = previousMemory, previousRedis
		model.InvalidatePricingCache()
		constant.CountToken, constant.ErrorLogEnabled, common.LogConsumeEnabled = previousCount, previousErrors, previousConsume
		common.BatchUpdateEnabled = previousBatch
		constant.StreamingTimeout = previousStreamingTimeout
		operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = previousFreePreConsume
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, sqlDB.Close())
	})
	var primaryCalls, backupCalls, thirdCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		primaryCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if scenario == "timeout" {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"discarded-attempt\"}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"discarded-attempt\"}}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":1},\"output\":[]}}\n\n")
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		backupCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if scenario == "third-channel" || scenario == "exhausted" {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":1},\"output\":[]}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"success\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n")
	}))
	defer backup.Close()
	third := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		thirdCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if scenario == "exhausted" {
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":1},\"output\":[]}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"success\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n")
	}))
	defer third.Close()
	require.NoError(t, database.Create(&model.User{Id: 99901, Username: "stream-test", Group: "default", Quota: 123456}).Error)
	primaryPriority, backupPriority := int64(10), int64(5)
	primaryChannel := model.Channel{Id: 901, Type: constant.ChannelTypeOpenAI, Key: "fixture-key", BaseURL: common.GetPointer(primary.URL), Status: common.ChannelStatusEnabled, Models: "recovery-integration", Group: "default", Priority: &primaryPriority}
	backupChannel := model.Channel{Id: 902, Type: constant.ChannelTypeOpenAI, Key: "fixture-key", BaseURL: common.GetPointer(backup.URL), Status: common.ChannelStatusEnabled, Models: "recovery-integration", Group: "default", Priority: &backupPriority}
	require.NoError(t, database.Create(&primaryChannel).Error)
	require.NoError(t, database.Create(&backupChannel).Error)
	require.NoError(t, database.Create(&model.Ability{ChannelId: 901, Group: "default", Model: "recovery-integration", Enabled: true, Priority: &primaryPriority}).Error)
	require.NoError(t, database.Create(&model.Ability{ChannelId: 902, Group: "default", Model: "recovery-integration", Enabled: true, Priority: &backupPriority}).Error)
	thirdPriority := int64(1)
	require.NoError(t, database.Create(&model.Channel{Id: 903, Type: constant.ChannelTypeOpenAI, Key: "fixture-key", BaseURL: common.GetPointer(third.URL), Status: common.ChannelStatusEnabled, Models: "recovery-integration", Group: "default", Priority: &thirdPriority}).Error)
	require.NoError(t, database.Create(&model.Ability{ChannelId: 903, Group: "default", Model: "recovery-integration", Enabled: true, Priority: &thirdPriority}).Error)
	model.InitChannelCache()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"recovery-integration","input":"private integration prompt","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 99901)
	c.Set("username", "stream-test")
	c.Set("group", "default")
	c.Set("using_group", "default")
	c.Set("token_group", "default")
	c.Set(common.RequestIdKey, "recovery-request-id")
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, &primaryChannel, "recovery-integration"))
	Relay(c, types.RelayFormatOpenAIResponses)
	assert.EqualValues(t, 1, primaryCalls.Load())
	assert.EqualValues(t, 1, backupCalls.Load())
	assert.NotContains(t, recorder.Body.String(), "discarded-attempt")
	var logs []model.Log
	require.NoError(t, database.Order("id").Find(&logs).Error)
	wantAttempts, wantChannel := 2, 902
	if scenario == "third-channel" || scenario == "exhausted" {
		wantAttempts, wantChannel = 3, 903
		assert.EqualValues(t, 1, thirdCalls.Load())
	} else {
		assert.Zero(t, thirdCalls.Load())
	}
	require.Len(t, logs, wantAttempts, "failed attempts are logged; only usable output is settled")
	assert.Equal(t, model.LogTypeError, logs[0].Type)
	assert.Equal(t, 901, logs[0].ChannelId)
	for _, log := range logs {
		assert.Equal(t, "recovery-request-id", log.RequestId)
		assert.NotContains(t, log.Other, "private integration prompt")
	}
	if scenario == "exhausted" {
		assert.NotContains(t, recorder.Body.String(), "success")
		assert.Contains(t, recorder.Body.String(), "error")
		for _, log := range logs {
			assert.Equal(t, model.LogTypeError, log.Type, "empty attempts must not produce a consume row")
		}
		return
	}
	assert.Equal(t, 200, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "success")
	assert.NotContains(t, recorder.Body.String(), `"error"`, "intermediate errors must not reach the client")
	finalLog := logs[len(logs)-1]
	assert.Equal(t, model.LogTypeConsume, finalLog.Type)
	assert.Equal(t, wantChannel, finalLog.ChannelId)
	assert.Equal(t, 5, finalLog.CompletionTokens)
	assert.Contains(t, finalLog.Other, "first_content_ms")
	if scenario == "timeout" {
		assert.Contains(t, finalLog.Other, "first_content_timeout")
	} else {
		assert.Contains(t, finalLog.Other, "stream_empty")
	}
}

func TestProcessChannelErrorUsesSnapshotWithoutLeakingChannelMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()
	previousErrorLogEnabled := constant.ErrorLogEnabled

	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Log{}))
	model.DB, model.LOG_DB = database, database
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	constant.ErrorLogEnabled = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
		constant.ErrorLogEnabled = previousErrorLogEnabled
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, database.Create(&model.User{Id: 7, Username: "log-owner", Group: "default"}).Error)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("id", 7)
	ctx.Set("username", "log-owner")
	ctx.Set("token_name", "test-token")
	ctx.Set("token_id", 11)
	ctx.Set("original_model", "gpt-test")
	ctx.Set("group", "default")
	ctx.Set("channel_id", 202)
	ctx.Set("channel_name", "mutable-context-channel")
	ctx.Set("channel_type", 9)
	ctx.Set("use_channel", []string{"101"})
	common.SetContextKey(ctx, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))

	channelSnapshot := types.ChannelError{
		ChannelId:   101,
		ChannelType: 1,
		ChannelName: "snapshot-channel",
		AutoBan:     false,
	}
	apiErr := types.NewOpenAIError(errors.New("upstream failed"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	processChannelError(ctx, channelSnapshot, apiErr, nil)

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Equal(t, channelSnapshot.ChannelId, stored.ChannelId)
	storedOther, err := common.StrToMap(stored.Other)
	require.NoError(t, err)
	assert.Equal(t, float64(http.StatusBadGateway), storedOther["status_code"])
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, storedOther, key)
	}
	adminInfo, ok := storedOther["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"101"}, adminInfo["use_channel"])

	logs, total, err := model.GetUserLogs(7, model.LogTypeError, 0, 0, "", "", 0, 10, "", "", "")
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, logs, 1)
	assert.Equal(t, channelSnapshot.ChannelId, logs[0].ChannelId)
	assert.Empty(t, logs[0].ChannelName)
	userOther, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	assert.NotContains(t, userOther, "admin_info")
	for _, key := range []string{"channel_id", "channel_name", "channel_type"} {
		assert.NotContains(t, userOther, key)
	}
}
