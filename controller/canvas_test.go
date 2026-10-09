package controller

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestRequireCanvasIntegrationSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CANVAS_INTEGRATION_SECRET", "test-canvas-secret")

	for name, tc := range map[string]struct {
		header string
		wantOK bool
	}{
		"missing": {wantOK: false},
		"wrong":   {header: "wrong", wantOK: false},
		"header":  {header: "test-canvas-secret", wantOK: true},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/canvas/key/ensure", nil)
			if tc.header != "" {
				req.Header.Set("X-Canvas-Integration-Secret", tc.header)
			}
			res := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(res)
			ctx.Request = req
			require.Equal(t, tc.wantOK, requireCanvasIntegrationSecret(ctx))
			if tc.wantOK {
				require.Equal(t, 200, res.Code)
			} else {
				require.Equal(t, 401, res.Code)
			}
		})
	}
}

func TestRequireCanvasIntegrationSecretBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CANVAS_INTEGRATION_SECRET", "test-canvas-secret")
	req := httptest.NewRequest("POST", "/api/canvas/sso/exchange", nil)
	req.Header.Set("Authorization", "Bearer test-canvas-secret")
	res := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(res)
	ctx.Request = req
	require.True(t, requireCanvasIntegrationSecret(ctx))
	require.Equal(t, 200, res.Code)
}

func TestCanvasIntegrationTokenDatabases(t *testing.T) {
	originalAutoGroups := setting.AutoGroups2JsonString()
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["OpenAI-gpt","imge-2-4k","video","Nano Banana"]`))
	t.Cleanup(func() { require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups)) })
	for _, database := range []struct {
		name string
		env  string
		kind common.DatabaseType
		open func(string) gorm.Dialector
	}{
		{name: "sqlite", kind: common.DatabaseTypeSQLite, open: sqlite.Open},
		{name: "mysql", env: "CANVAS_TEST_MYSQL_DSN", kind: common.DatabaseTypeMySQL, open: mysql.Open},
		{name: "postgres", env: "CANVAS_TEST_POSTGRES_DSN", kind: common.DatabaseTypePostgreSQL, open: postgres.Open},
	} {
		t.Run(database.name, func(t *testing.T) {
			dsn := ":memory:"
			if database.env != "" {
				dsn = strings.TrimSpace(os.Getenv(database.env))
				if dsn == "" {
					t.Skip(database.env + " is not configured")
				}
			}
			db, err := gorm.Open(database.open(dsn), &gorm.Config{NamingStrategy: schema.NamingStrategy{
				TablePrefix: fmt.Sprintf("canvas_test_%d_", time.Now().UnixNano()),
			}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousKind := model.DB, common.MainDatabaseType()
			model.DB = db
			common.SetMainDatabaseType(database.kind)
			t.Cleanup(func() {
				model.DB = previousDB
				common.SetMainDatabaseType(previousKind)
				require.NoError(t, db.Migrator().DropTable(&model.Token{}, &model.User{}))
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
			versionSQL := "SELECT version()"
			if database.kind == common.DatabaseTypeSQLite {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Logf("database: %s", version)
			user := model.User{Username: "canvas-user", Password: "test-only", Status: common.UserStatusEnabled, Group: "paid", Quota: 123456}
			require.NoError(t, db.Create(&user).Error)
			token, created, err := model.EnsureCanvasIntegrationToken(user.Id)
			require.NoError(t, err)
			assert.True(t, created)
			assert.Equal(t, "auto", token.Group)
			assert.Equal(t, user.Id, token.UserId)
			assert.True(t, token.UnlimitedQuota)
			assert.True(t, token.CrossGroupRetry)
			assert.JSONEq(t, `["OpenAI-gpt","imge-2-4k","video","Nano Banana"]`, token.AutoGroups)

			// A user may restrict their dedicated key. Reuse must not reset those choices.
			require.NoError(t, db.Model(token).Updates(map[string]any{"group": "paid", "model_limits_enabled": true, "model_limits": "allowed-model"}).Error)
			reused, created, err := model.EnsureCanvasIntegrationToken(user.Id)
			require.NoError(t, err)
			assert.False(t, created)
			assert.Equal(t, token.Id, reused.Id)
			assert.Equal(t, "auto", reused.Group)
			assert.True(t, reused.CrossGroupRetry)
			assert.JSONEq(t, `["OpenAI-gpt","imge-2-4k","video","Nano Banana"]`, reused.AutoGroups)
			assert.True(t, reused.ModelLimitsEnabled)
			assert.Equal(t, "allowed-model", reused.ModelLimits)

			for _, change := range []struct {
				name   string
				fields map[string]any
			}{
				{name: "disabled", fields: map[string]any{"status": common.TokenStatusDisabled}},
				{name: "expired", fields: map[string]any{"status": common.TokenStatusEnabled, "expired_time": common.GetTimestamp() - 1}},
				{name: "exhausted", fields: map[string]any{"expired_time": -1, "unlimited_quota": false, "remain_quota": 0}},
			} {
				t.Run(change.name, func(t *testing.T) {
					require.NoError(t, db.Model(&model.Token{}).Where("id = ?", token.Id).Updates(change.fields).Error)
					result, created, err := model.EnsureCanvasIntegrationToken(user.Id)
					require.Error(t, err)
					assert.Nil(t, result)
					assert.False(t, created)
				})
			}
			var count int64
			require.NoError(t, db.Model(&model.Token{}).Where("user_id = ?", user.Id).Count(&count).Error)
			assert.EqualValues(t, 1, count, "unavailable keys must not be replaced behind the user's back")
			var preserved model.User
			require.NoError(t, db.First(&preserved, user.Id).Error)
			assert.Equal(t, user.Quota, preserved.Quota, "key provisioning must not change the wallet")
			other := model.User{Username: "other-user", Password: "test-only", Status: common.UserStatusEnabled, Group: "other", AffCode: "other-test-code", Quota: 654321}
			require.NoError(t, db.Create(&other).Error)
			otherToken, otherCreated, err := model.EnsureCanvasIntegrationToken(other.Id)
			require.NoError(t, err)
			assert.True(t, otherCreated)
			assert.Equal(t, other.Id, otherToken.UserId)
			assert.NotEqual(t, token.Key, otherToken.Key)
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", other.Id).Update("status", common.UserStatusDisabled).Error)
			_, _, err = model.EnsureCanvasIntegrationToken(other.Id)
			require.Error(t, err, "disabled users must not retrieve or create a key")
		})
	}
}

func TestCanvasSSOTicketLifecycle(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("CANVAS_TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("CANVAS_TEST_REDIS_ADDR is not configured")
	}
	gin.SetMode(gin.TestMode)
	t.Setenv("CANVAS_INTEGRATION_SECRET", "canvas-test-secret")
	t.Setenv("CANVAS_PUBLIC_URL", "https://canvas.ailili.test")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	user := model.User{Username: "ticket-user", Password: "test-only", Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	previousDB, previousRedis, previousEnabled := model.DB, common.RDB, common.RedisEnabled
	model.DB, common.RDB, common.RedisEnabled = db, redis.NewClient(&redis.Options{Addr: address}), true
	require.NoError(t, common.RDB.Ping(context.Background()).Err())
	t.Cleanup(func() {
		require.NoError(t, common.RDB.Close())
		model.DB, common.RDB, common.RedisEnabled = previousDB, previousRedis, previousEnabled
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	})

	for _, test := range []struct {
		name    string
		expire  bool
		disable bool
	}{
		{name: "single_use"}, {name: "expired", expire: true}, {name: "disabled_user", disable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).Update("status", common.UserStatusEnabled).Error)
			launchResponse := httptest.NewRecorder()
			launch, _ := gin.CreateTestContext(launchResponse)
			launch.Request = httptest.NewRequest("POST", "/api/canvas/sso/launch", nil)
			launch.Set("id", user.Id)
			GetCanvasSSOLaunch(launch)
			require.Equal(t, 200, launchResponse.Code)
			var ticket struct {
				Code               string `json:"code"`
				RedirectURL        string `json:"redirect_url"`
				ExpiresIn          int    `json:"expires_in"`
				ProvisioningTicket string `json:"provisioning_ticket"`
			}
			require.NoError(t, common.Unmarshal(launchResponse.Body.Bytes(), &ticket))
			require.NotEmpty(t, ticket.Code)
			assert.Equal(t, 60, ticket.ExpiresIn)
			assert.Contains(t, ticket.RedirectURL, "https://canvas.ailili.test/auth/canvas/callback?code=")
			if test.expire {
				require.NoError(t, common.RDB.Expire(context.Background(), canvasTicketStorageKey(ticket.Code), -time.Second).Err())
			}
			if test.disable {
				require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).Update("status", common.UserStatusDisabled).Error)
			}
			body, err := common.Marshal(map[string]string{"code": ticket.Code})
			require.NoError(t, err)
			unauthorized, _ := gin.CreateTestContext(httptest.NewRecorder())
			unauthorized.Request = httptest.NewRequest("POST", "/api/canvas/sso/exchange", strings.NewReader(string(body)))
			ExchangeCanvasSSOTicket(unauthorized)
			assert.Equal(t, 401, unauthorized.Writer.Status())
			for attempt := range 2 {
				response := httptest.NewRecorder()
				request, _ := gin.CreateTestContext(response)
				request.Request = httptest.NewRequest("POST", "/api/canvas/sso/exchange", strings.NewReader(string(body)))
				request.Request.Header.Set("Content-Type", "application/json")
				request.Request.Header.Set("X-Canvas-Integration-Secret", "canvas-test-secret")
				ExchangeCanvasSSOTicket(request)
				if attempt == 0 && !test.expire && !test.disable {
					assert.Equal(t, 200, response.Code)
					var identity struct {
						ProvisioningTicket string `json:"provisioning_ticket"`
						User               struct {
							ID int `json:"id"`
						} `json:"user"`
					}
					require.NoError(t, common.Unmarshal(response.Body.Bytes(), &identity))
					assert.Equal(t, user.Id, identity.User.ID)
					assert.NotEmpty(t, identity.ProvisioningTicket)
					assert.NotContains(t, response.Body.String(), ticket.Code)

					arbitraryProfileBody, marshalErr := common.Marshal(map[string]any{"user_id": user.Id})
					require.NoError(t, marshalErr)
					arbitraryProfileResponse := httptest.NewRecorder()
					arbitraryProfile, _ := gin.CreateTestContext(arbitraryProfileResponse)
					arbitraryProfile.Request = httptest.NewRequest("POST", "/api/canvas/profile", strings.NewReader(string(arbitraryProfileBody)))
					arbitraryProfile.Request.Header.Set("Content-Type", "application/json")
					arbitraryProfile.Request.Header.Set("X-Canvas-Integration-Secret", "canvas-test-secret")
					GetCanvasIntegrationProfile(arbitraryProfile)
					assert.Equal(t, 400, arbitraryProfileResponse.Code, "the integration secret cannot select an arbitrary user")

					profileBody, marshalErr := common.Marshal(map[string]string{"ticket": identity.ProvisioningTicket})
					require.NoError(t, marshalErr)
					profileResponse := httptest.NewRecorder()
					profile, _ := gin.CreateTestContext(profileResponse)
					profile.Request = httptest.NewRequest("POST", "/api/canvas/profile", strings.NewReader(string(profileBody)))
					profile.Request.Header.Set("Content-Type", "application/json")
					profile.Request.Header.Set("X-Canvas-Integration-Secret", "canvas-test-secret")
					GetCanvasIntegrationProfile(profile)
					assert.Equal(t, 200, profileResponse.Code)

					keyBody, marshalErr := common.Marshal(map[string]string{"ticket": identity.ProvisioningTicket, "purpose": "canvas"})
					require.NoError(t, marshalErr)
					keyResponse := httptest.NewRecorder()
					keyRequest, _ := gin.CreateTestContext(keyResponse)
					keyRequest.Request = httptest.NewRequest("POST", "/api/canvas/key/ensure", strings.NewReader(string(keyBody)))
					keyRequest.Request.Header.Set("Content-Type", "application/json")
					keyRequest.Request.Header.Set("X-Canvas-Integration-Secret", "canvas-test-secret")
					EnsureCanvasAPIKey(keyRequest)
					assert.Equal(t, 200, keyResponse.Code)
					assert.NotContains(t, keyResponse.Body.String(), identity.ProvisioningTicket)
				} else {
					assert.Equal(t, 401, response.Code)
				}
			}
		})
	}
}
