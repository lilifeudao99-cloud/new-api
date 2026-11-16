package model

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStreamRecoveryExclusionsAcrossDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := "local"
			if dialect != "sqlite" {
				dsn = os.Getenv(map[string]string{"mysql": "TEST_MYSQL_DSN", "postgres": "TEST_POSTGRES_DSN"}[dialect])
				if dsn == "" {
					t.Skip("test database DSN is not configured")
				}
			}
			previousDB, previousCache := DB, common.MemoryCacheEnabled
			previousMain, previousLog, previousPath := common.MainDatabaseType(), common.LogDatabaseType(), common.SQLitePath
			common.SQLitePath = filepath.Join(t.TempDir(), "stream-recovery.db")
			t.Setenv("STREAM_RECOVERY_TEST_DSN", dsn)
			database, dbType, err := chooseDB("STREAM_RECOVERY_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := database.DB()
			require.NoError(t, err)
			t.Cleanup(func() {
				DB, common.MemoryCacheEnabled, common.SQLitePath = previousDB, previousCache, previousPath
				common.SetDatabaseTypes(previousMain, previousLog)
				initCol()
				InitChannelCache()
				require.NoError(t, sqlDB.Close())
			})
			common.SetMainDatabaseType(dbType)
			initCol()
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, database.Raw(versionQuery).Scan(&version).Error)
			t.Logf("%s: %s", dialect, version)
			require.NoError(t, database.AutoMigrate(&Channel{}, &Ability{}))
			DB = database.Begin()
			require.NoError(t, DB.Error)
			t.Cleanup(func() { require.NoError(t, DB.Rollback().Error) })
			for _, spec := range []struct {
				id                 int
				group, model, path string
				priority           int64
			}{
				{9201, "vip,default", "stream-model", "/v1/responses", 100},
				{9202, "vip", "stream-model", "/v1/chat/completions", 90},
				{9203, "vip", "other-model", "/v1/responses", 80},
				{9204, "unauthorized", "stream-model", "/v1/responses", 70},
				{9205, "vip", "stream-model", "/v1/responses", 60},
				{9206, "vip", "stream-model", "/v1/responses", 50},
			} {
				ch := Channel{Id: spec.id, Name: "stream-fixture", Key: "fixture-only", Type: constant.ChannelTypeAdvancedCustom,
					Status: common.ChannelStatusEnabled, Group: spec.group, Models: spec.model, Priority: &spec.priority}
				ch.SetOtherSettings(kitdto.ChannelOtherSettings{AdvancedCustom: &kitdto.AdvancedCustomConfig{
					Routes: []kitdto.AdvancedCustomRoute{{IncomingPath: spec.path, Models: []string{spec.model}}},
				}})
				require.NoError(t, DB.Create(&ch).Error)
				require.NoError(t, ch.AddAbilities(DB))
			}
			for _, cached := range []bool{false, true} {
				t.Run(map[bool]string{false: "database", true: "cache"}[cached], func(t *testing.T) {
					common.MemoryCacheEnabled = cached
					InitChannelCache()
					filters := []dto.ChannelFilter{
						{Kind: dto.FilterRequestPath, RequestPath: "/v1/responses"},
						{Kind: dto.FilterExcludeChannels, ExcludedChannelIDs: []int{9201}},
					}
					for _, want := range []int{9205, 9206} {
						ch, err := GetRandomSatisfiedChannel("vip", "stream-model", 0, filters)
						require.NoError(t, err)
						require.NotNil(t, ch)
						assert.Equal(t, want, ch.Id)
						filters[1].ExcludedChannelIDs = append(filters[1].ExcludedChannelIDs, ch.Id)
					}
					ch, err := GetRandomSatisfiedChannel("vip", "stream-model", 0, filters)
					require.NoError(t, err)
					assert.Nil(t, ch, "do not replay failed channels or bypass path/model/group constraints")
					ch, err = GetRandomSatisfiedChannel("default", "stream-model", 0, filters)
					require.NoError(t, err)
					assert.Nil(t, ch, "the same channel in another group is still excluded")
				})
			}
		})
	}
}

func TestStreamRecoveryExclusionsFailClosedOnChannelLookupError(t *testing.T) {
	const callback = "stream_recovery_test:channel_lookup_error"
	require.NoError(t, DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Channel" {
			tx.AddError(errors.New("fixture channel lookup unavailable"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, DB.Callback().Query().Remove(callback)) })
	got := filterAbilitiesByConstraints([]Ability{{ChannelId: 9201}}, "stream-model", []dto.ChannelFilter{
		{Kind: dto.FilterExcludeChannels, ExcludedChannelIDs: []int{9201}},
	})
	assert.Empty(t, got, "lookup errors must never permit a failed channel to be replayed")
}

func TestFilterCandidateIDs(t *testing.T) {
	alphaSetting := `{"task_plugin_key":"alpha"}`
	betaSetting := `{"task_plugin_key":"beta"}`
	alpha := &Channel{Id: 900001, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Setting: &alphaSetting}
	beta := &Channel{Id: 900002, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Setting: &betaSetting}
	ordinary := &Channel{Id: 900003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	kling := &Channel{Id: 900004, Type: constant.ChannelTypeKling, Status: common.ChannelStatusEnabled}
	jimeng := &Channel{Id: 900005, Type: constant.ChannelTypeJimeng, Status: common.ChannelStatusEnabled}
	matchingCustom := &Channel{Id: 900010, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled}
	matchingCustom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/chat/completions",
				Models:       []string{"gpt-4"},
			}},
		},
	})
	otherCustom := &Channel{Id: 900011, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled}
	otherCustom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/responses",
				Models:       []string{"gpt-4"},
			}},
		},
	})

	pathFilter := dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: "/v1/chat/completions"}
	emptyPathFilter := dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: ""}

	tests := []struct {
		name      string
		ids       []int
		modelName string
		filters   []dto.ChannelFilter
		wantKept  []int
		wantEmpty dto.ChannelFilterKind
	}{
		{
			name:      "identity keeps matching type-59 key",
			ids:       []int{900001, 900002},
			modelName: "shared",
			filters:   identityFilters("alpha", nil),
			wantKept:  []int{900001},
		},
		{
			name:      "identity empty key drops all type-59",
			ids:       []int{900001, 900002},
			modelName: "shared",
			filters:   identityFilters("", nil),
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "identity empty key keeps ordinary channel",
			ids:       []int{900003},
			modelName: "ordinary",
			filters:   identityFilters("", nil),
			wantKept:  []int{900003},
		},
		{
			name:      "identity keeps matching legacy type",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}),
			wantKept:  []int{900004},
		},
		{
			name:      "identity keeps all listed legacy types",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling, constant.ChannelTypeJimeng}),
			wantKept:  []int{900004, 900005},
		},
		{
			name:      "identity keyed with no types drops legacy",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", nil),
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "identity drops missing cache entry",
			ids:       []int{900004, 999999},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}),
			wantKept:  []int{900004},
		},
		{
			name:      "empty request path is a passthrough including missing ids",
			ids:       []int{900003, 900010, 999999},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{emptyPathFilter},
			wantKept:  []int{900003, 900010, 999999},
		},
		{
			name:      "request path keeps missing cache entry for consistency",
			ids:       []int{900003, 999999},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{900003, 999999},
		},
		{
			name:      "request path keeps matching type-58 and ordinary",
			ids:       []int{900003, 900010, 900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{900003, 900010},
		},
		{
			name:      "request path empties when only unmatched type-58 remains",
			ids:       []int{900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{},
			wantEmpty: dto.FilterRequestPath,
		},
		{
			name:      "intersection attributes empty set to identity after path keeps candidates",
			ids:       []int{900001, 900010},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter, identityFilters("missing", nil)[0]},
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "intersection attributes empty set to path when path runs first",
			ids:       []int{900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{identityFilters("", nil)[0], pathFilter},
			wantKept:  []int{},
			wantEmpty: dto.FilterRequestPath,
		},
	}

	channelSyncLock.Lock()
	previous := channelsIDM
	channelsIDM = map[int]*Channel{
		900001: alpha,
		900002: beta,
		900003: ordinary,
		900004: kling,
		900005: jimeng,
		900010: matchingCustom,
		900011: otherCustom,
	}
	t.Cleanup(func() {
		channelsIDM = previous
		channelSyncLock.Unlock()
	})

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			kept, emptiedBy := filterCandidateIDs(testCase.ids, testCase.modelName, testCase.filters)
			if testCase.wantKept == nil {
				assert.Nil(t, kept)
			} else {
				assert.Equal(t, testCase.wantKept, kept)
			}
			assert.Equal(t, testCase.wantEmpty, emptiedBy)
		})
	}
}

func TestChannelSatisfiesFilters(t *testing.T) {
	alphaSetting := `{"task_plugin_key":"alpha"}`
	alpha := &Channel{Id: 1, Type: constant.ChannelTypeTaskPlugin, Setting: &alphaSetting}
	ordinary := &Channel{Id: 2, Type: constant.ChannelTypeOpenAI}
	custom := &Channel{Id: 3, Type: constant.ChannelTypeAdvancedCustom}
	custom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/chat/completions",
				Models:       []string{"gpt-4"},
			}},
		},
	})

	ok, kind := ChannelSatisfiesFilters(nil, "gpt-4", nil)
	assert.False(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(alpha, "shared", identityFilters("alpha", nil))
	require.True(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(alpha, "shared", identityFilters("beta", nil))
	assert.False(t, ok)
	assert.Equal(t, dto.FilterTaskPluginIdentity, kind)

	ok, kind = ChannelSatisfiesFilters(ordinary, "gpt-4", []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: "/v1/chat/completions",
	}})
	require.True(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(custom, "gpt-4", []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: "/v1/responses",
	}})
	assert.False(t, ok)
	assert.Equal(t, dto.FilterRequestPath, kind)
}
