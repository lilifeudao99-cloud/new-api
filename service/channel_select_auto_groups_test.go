package service

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStreamRecoverySelectsUntriedAuthorizedChannels(t *testing.T) {
	for _, tc := range []struct {
		name, tokenGroup                                    string
		crossGroup, sameGroup, revoked, missingCurrent, pin bool
		wantID                                              int
		wantGroup                                           string
	}{
		{name: "fixed group prefers untried peer", tokenGroup: "vip", sameGroup: true, wantID: 2102, wantGroup: "vip"},
		{name: "fixed group cannot borrow another group", tokenGroup: "vip", wantGroup: "vip"},
		{name: "auto prefers actual current group over eagerly advanced cursor", tokenGroup: "auto", crossGroup: true, sameGroup: true, wantID: 2102, wantGroup: "vip"},
		{name: "auto can use authorized next group", tokenGroup: "auto", crossGroup: true, wantID: 2103, wantGroup: "default"},
		{name: "auto without cross group permission stays put", tokenGroup: "auto", wantGroup: "vip"},
		{name: "auto cannot use a group outside token list", tokenGroup: "auto", crossGroup: true, revoked: true, wantGroup: "vip"},
		{name: "auto with unknown current group fails closed", tokenGroup: "auto", crossGroup: true, missingCurrent: true, wantGroup: "removed"},
		{name: "pinned channel cannot fail over", tokenGroup: "auto", crossGroup: true, pin: true, wantGroup: "vip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupChannelSelectAutoGroupsTest(t)
			// A large legacy retry setting must not keep a failed stream in vip.
			common.RetryTimes = 100
			createChannelSelectAutoGroupsChannel(t, db, 2101, "vip", "gpt-6.1-sol")
			if tc.sameGroup {
				createChannelSelectAutoGroupsChannel(t, db, 2102, "vip", "gpt-6.1-sol")
			}
			createChannelSelectAutoGroupsChannel(t, db, 2103, "default", "gpt-6.1-sol")
			model.InitChannelCache()
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
			groups := []string{"vip", "default"}
			if tc.revoked {
				groups = []string{"vip"}
			}
			common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, groups)
			common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, tc.crossGroup)
			common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "vip")
			if tc.missingCurrent {
				common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "removed")
			}
			common.SetContextKey(ctx, constant.ContextKeyAutoGroupIndex, 1)
			if tc.pin {
				GetChannelConstraints(ctx).AddPin(dto.ChannelPin{ChannelId: 2101, Source: dto.PinSourceOriginTask, RetryMode: dto.PinRetrySameChannel})
			}
			param := &RetryParam{Ctx: ctx, TokenGroup: tc.tokenGroup, ModelName: "gpt-6.1-sol", Retry: common.GetPointer(1), ExcludeChannelIDs: []int{2101}}
			ch, group, err := CacheGetRandomSatisfiedChannel(param)
			require.NoError(t, err)
			assert.Equal(t, tc.wantGroup, group)
			if tc.wantID == 0 {
				assert.Nil(t, ch)
				return
			}
			require.NotNil(t, ch)
			assert.Equal(t, tc.wantID, ch.Id)
			assert.Empty(t, GetChannelConstraints(ctx).Filters, "request-scoped exclusions must not mutate shared constraints")
			param.ExcludeChannelIDs = append(param.ExcludeChannelIDs, ch.Id)
			ch, _, err = CacheGetRandomSatisfiedChannel(param)
			require.NoError(t, err)
			if tc.tokenGroup == "auto" && tc.sameGroup {
				require.NotNil(t, ch)
				assert.Equal(t, 2103, ch.Id)
			} else {
				assert.Nil(t, ch)
			}
		})
	}
}

func setupChannelSelectAutoGroupsTest(t *testing.T) *gorm.DB {
	t.Helper()

	originalDB := model.DB
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalRetryTimes := common.RetryTimes
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalGroupRatios := ratio_setting.GroupRatio2JSONString()
	originalMaxTokenAutoGroups := setting.GetMaxTokenAutoGroups()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	common.RetryTimes = 0

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":2}`))
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.RetryTimes = originalRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalGroupRatios))
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMaxTokenAutoGroups)))

		if originalMemoryCacheEnabled && originalDB != nil &&
			originalDB.Migrator().HasTable(&model.Channel{}) && originalDB.Migrator().HasTable(&model.Ability{}) {
			model.InitChannelCache()
		}
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})

	return db
}

func createChannelSelectAutoGroupsChannel(t *testing.T, db *gorm.DB, id int, group, modelName string) {
	t.Helper()
	priority := int64(0)
	weight := uint(100)
	require.NoError(t, db.Create(&model.Channel{
		Id:       id,
		Type:     constant.ChannelTypeOpenAI,
		Key:      fmt.Sprintf("key-%d", id),
		Status:   common.ChannelStatusEnabled,
		Name:     fmt.Sprintf("channel-%d", id),
		Weight:   &weight,
		Models:   modelName,
		Group:    group,
		Priority: &priority,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     group,
		Model:     modelName,
		ChannelId: id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}).Error)
}

func TestCacheGetRandomSatisfiedChannelUsesTokenAutoGroupsWhenGlobalAutoIsEmpty(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	const modelName = "auto-groups-runtime-model"
	createChannelSelectAutoGroupsChannel(t, db, 2101, "vip", modelName)
	createChannelSelectAutoGroupsChannel(t, db, 2102, "default", modelName)
	model.InitChannelCache()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", "default"})
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)

	retry := 0
	param := &RetryParam{
		Ctx:         ctx,
		TokenGroup:  "auto",
		ModelName:   modelName,
		RequestPath: "/v1/chat/completions",
		Retry:       &retry,
	}

	first, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, 2101, first.Id)
	assert.Equal(t, "vip", selectedGroup)
	assert.Equal(t, "vip", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
	assert.Empty(t, setting.GetAutoGroups(), "the selection must not depend on the global Auto list")

	param.IncreaseRetry()
	second, selectedGroup, err := CacheGetRandomSatisfiedChannel(param)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, 2102, second.Id)
	assert.Equal(t, "default", selectedGroup)
	assert.Equal(t, "default", common.GetContextKeyString(ctx, constant.ContextKeyAutoGroup))
}
