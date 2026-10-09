package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEnsureCanvasIntegrationTokenUsesConfiguredAutoGroups(t *testing.T) {
	previousDB, previousKind := DB, common.MainDatabaseType()
	originalAutoGroups := setting.AutoGroups2JsonString()
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousKind)
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
	})

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["OpenAI-gpt","imge-2-4k","video","Nano Banana"]`))
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))

	user := User{Username: "canvas-auto-groups", Password: "test-only", Status: common.UserStatusEnabled, Group: "paid"}
	require.NoError(t, db.Create(&user).Error)

	token, created, err := EnsureCanvasIntegrationToken(user.Id)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "auto", token.Group)
	require.True(t, token.CrossGroupRetry)
	require.JSONEq(t, `["OpenAI-gpt","imge-2-4k","video","Nano Banana"]`, token.AutoGroups)
	require.Equal(t, user.Id, token.UserId)
	require.True(t, token.UnlimitedQuota)

	// Existing managed keys are synchronized to the new ordered Auto policy,
	// without replacing the key or changing unrelated user-configured limits.
	require.NoError(t, db.Model(token).Updates(map[string]any{
		"group": "paid", "cross_group_retry": false,
		"auto_groups": `[]`, "model_limits_enabled": true,
		"model_limits": "user-model-restriction",
	}).Error)
	reused, created, err := EnsureCanvasIntegrationToken(user.Id)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, token.Id, reused.Id)
	require.Equal(t, token.Key, reused.Key)
	require.Equal(t, "auto", reused.Group)
	require.True(t, reused.CrossGroupRetry)
	require.JSONEq(t, `["OpenAI-gpt","imge-2-4k","video","Nano Banana"]`, reused.AutoGroups)
	require.True(t, reused.ModelLimitsEnabled)
	require.Equal(t, "user-model-restriction", reused.ModelLimits)

	require.NoError(t, sqlDB.Close())
}
