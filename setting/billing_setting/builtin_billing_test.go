package billing_setting_test

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGPT6AstraBuiltinBilling(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	savedOptions := common.OptionMap
	t.Cleanup(func() {
		*settings, common.OptionMap = saved, savedOptions
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	common.OptionMap = map[string]string{"billing_setting.billing_mode": `{}`, "billing_setting.billing_expr": `{}`}
	require.NoError(t, config.GlobalConfig.LoadFromDB(common.OptionMap))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	assert.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("gpt-6-astra"))
	expression, ok := billing_setting.GetBillingExpr("gpt-6-astra")
	require.True(t, ok)

	for _, tc := range []struct {
		name                           string
		input, output, cached, written int
		request                        string
		quota                          int
	}{
		{"standard", 1000, 100, 0, 0, `{}`, 7500},
		{"client flex cannot discount standard pricing", 1000, 100, 0, 0, `{"service_tier":"flex"}`, 7500},
		{"cache at context boundary", 272000, 1000, 200000, 20000, `{}`, 510000},
		{"whole request above boundary", 272001, 1000, 200000, 20000, `{}`, 1007510},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens: tc.input, CompletionTokens: tc.output,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: tc.cached, CacheWriteTokens: tc.written},
			}
			params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
			result, err := billingexpr.ComputeTieredQuotaWithRequest(&billingexpr.BillingSnapshot{
				ExprString: expression, GroupRatio: 1, QuotaPerUnit: 500000,
			}, params, billingexpr.RequestInput{Body: []byte(tc.request)})
			require.NoError(t, err)
			assert.Equal(t, tc.quota, result.ActualQuotaAfterGroup)
		})
	}

	t.Run("admin options expose defaults without persisting them", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		controller.GetOptions(ctx)
		var response struct {
			Success bool
			Data    []model.Option
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success)
		found := map[string]string{}
		for _, option := range response.Data {
			if _, ok := common.OptionMap[option.Key]; ok {
				assert.NotContains(t, found, option.Key)
				var values map[string]string
				require.NoError(t, common.UnmarshalJsonStr(option.Value, &values))
				found[option.Key] = values["gpt-6-astra"]
				assert.Equal(t, `{}`, common.OptionMap[option.Key])
			}
		}
		assert.Equal(t, map[string]string{"billing_setting.billing_mode": "tiered_expr", "billing_setting.billing_expr": expression}, found)
	})

	for _, tc := range []struct {
		name, mode, expr, ratios, prices, wantMode string
	}{
		{"custom expression overrides legacy price", "tiered_expr", "p * 7", `{"gpt-6-astra":8}`, `{}`, "tiered_expr"},
		{"explicit ratio mode", "ratio", "", `{}`, `{}`, "ratio"},
		{"existing free token price", "", "", `{"gpt-6-astra":0}`, `{}`, "ratio"},
		{"existing per-call price", "", "", `{}`, `{"gpt-6-astra":0.1}`, "ratio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			if tc.mode != "" {
				settings.BillingMode["gpt-6-astra"] = tc.mode
			}
			if tc.expr != "" {
				settings.BillingExpr["gpt-6-astra"] = tc.expr
			}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(tc.ratios))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(tc.prices))
			assert.Equal(t, tc.wantMode, billing_setting.GetBillingMode("gpt-6-astra"))
			actual, ok := billing_setting.GetBillingExpr("gpt-6-astra")
			assert.Equal(t, tc.expr, actual)
			assert.Equal(t, tc.expr != "", ok)
		})
	}
}

func TestGPT61SolBuiltinBilling(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	savedOptions := common.OptionMap
	t.Cleanup(func() {
		*settings, common.OptionMap = saved, savedOptions
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	common.OptionMap = map[string]string{"billing_setting.billing_mode": `{}`, "billing_setting.billing_expr": `{}`}
	require.NoError(t, config.GlobalConfig.LoadFromDB(common.OptionMap))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))

	assert.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode("gpt-6.1-sol"))
	expression, ok := billing_setting.GetBillingExpr("gpt-6.1-sol")
	require.True(t, ok)

	for _, tc := range []struct {
		name                           string
		input, output, cached, written int
		quota                          int
	}{
		{"standard rates including cache", 1000, 100, 200, 40, 1320},
		{"long-context rates apply to whole request", 272001, 100, 200, 40, 544392},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens: tc.input, CompletionTokens: tc.output,
				PromptTokensDetails: dto.InputTokenDetails{CachedTokens: tc.cached, CacheWriteTokens: tc.written},
			}
			params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
			result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{
				ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000,
			}, params)
			require.NoError(t, err)
			assert.Equal(t, tc.quota, result.ActualQuotaAfterGroup)
		})
	}
}

func TestGPT62BuiltinBillingRulesAndOverrides(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})

	for _, modelName := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		t.Run(modelName, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			expression, ok := billing_setting.GetBuiltinBillingExpr(modelName)
			require.True(t, ok)
			require.NoError(t, billing_setting.SmokeTestExpr(expression))
			assert.Equal(t, billing_setting.BillingModeTieredExpr, billing_setting.GetBillingMode(modelName))
			effective, ok := billing_setting.GetBillingExpr(modelName)
			require.True(t, ok)
			assert.Equal(t, expression, effective)

			for _, tc := range []struct {
				name                           string
				input, output, cached, written int
				written12h                     *int
				tier                           string
				solQuota, astraQuota           int
			}{
				{"standard cache prices", 1000, 100, 200, 40, nil, "standard", 1330, 6650},
				{"exact context boundary", 272000, 1000, 200000, 20000, nil, "standard", 102000, 510000},
				{"whole request above boundary", 272001, 1000, 200000, 20000, nil, "long_context", 201502, 1007510},
				{"cached input still counts toward context", 300000, 1000, 250000, 0, nil, "long_context", 157500, 787500},
				{"cache read is not also charged as ordinary input", 1000, 0, 1000, 0, nil, "standard", 100, 500},
				{"cache write is not also charged as ordinary input", 1000, 0, 0, 1000, nil, "standard", 1250, 6250},
				{"mixed cache lifetimes", 12000, 100, 4000, 7000, common.GetPointer(4000), "standard", 15650, 78250},
				{"12h writes above context boundary", 272001, 1000, 200000, 20000, common.GetPointer(10000), "long_context", 226502, 1132510},
				{"12h writes do not incur a second 30m or ordinary input charge", 1000, 0, 0, 1000, common.GetPointer(1000), "standard", 2500, 12500},
				{"explicit zero 12h writes", 1000, 0, 0, 1000, common.GetPointer(0), "standard", 1250, 6250},
			} {
				t.Run(tc.name, func(t *testing.T) {
					usage := &dto.Usage{
						PromptTokens: tc.input, CompletionTokens: tc.output,
						PromptTokensDetails: dto.InputTokenDetails{CachedTokens: tc.cached, CacheWriteTokens: tc.written},
					}
					if tc.written12h != nil {
						usage.PromptTokensDetails.CacheWriteTokensDetails = &dto.CacheWriteTokenDetails{
							TTL30mTokens: common.GetPointer(tc.written - *tc.written12h), TTL12hTokens: tc.written12h,
						}
					}
					params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
					result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{
						ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000,
					}, params)
					require.NoError(t, err)
					wantQuota := tc.solQuota
					if modelName == "gpt-6.2-astra" {
						wantQuota = tc.astraQuota
					}
					assert.Equal(t, wantQuota, result.ActualQuotaAfterGroup)
					assert.Equal(t, tc.tier, result.MatchedTier)
				})
			}

			legacy, err := common.Marshal(map[string]float64{modelName: 37.5})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(legacy)))
			assert.Equal(t, billing_setting.BillingModeRatio, billing_setting.GetBillingMode(modelName), "preserve existing administrator token pricing")
			_, ok = billing_setting.GetBillingExpr(modelName)
			assert.False(t, ok)

			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			price, err := common.Marshal(map[string]float64{modelName: 0})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(price)))
			assert.Equal(t, billing_setting.BillingModeRatio, billing_setting.GetBillingMode(modelName), "preserve an explicitly free administrator price")

			settings.BillingMode[modelName] = billing_setting.BillingModeTieredExpr
			settings.BillingExpr[modelName] = `tier("custom", p * 7 + c * 9)`
			effective, ok = billing_setting.GetBillingExpr(modelName)
			require.True(t, ok)
			assert.Equal(t, settings.BillingExpr[modelName], effective, "preserve the administrator's custom expression")
		})
	}
}

func TestGPT62CacheWriteUsageContract(t *testing.T) {
	// Official Responses usage example, with output added for settlement.
	var response dto.OpenAIResponsesResponse
	require.NoError(t, common.UnmarshalJsonStr(`{"id":"resp_cache","model":"gpt-6.2-sol","status":"completed","output":[],"usage":{"input_tokens":12000,"output_tokens":100,"total_tokens":12100,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000,"cache_write_tokens_details":{"ttl_30m_tokens":3000,"ttl_12h_tokens":4000}}}}`, &response))
	converted, err := relayconvert.ConvertResponse(nil, nil, types.RelayFormatOpenAI, &response)
	require.NoError(t, err)
	chat, ok := converted.Value.(*dto.OpenAITextResponse)
	require.True(t, ok)
	convertedBack, err := relayconvert.ConvertResponse(nil, nil, types.RelayFormatOpenAIResponses, chat)
	require.NoError(t, err)
	require.NotNil(t, convertedBack.Usage.InputTokensDetails)
	assert.Equal(t, response.Usage.InputTokensDetails, convertedBack.Usage.InputTokensDetails)

	usage := dto.MergeUsageNonZero(nil, converted.Usage)
	usage = dto.MergeUsageNonZero(usage, &dto.Usage{CompletionTokens: 100})
	require.NotNil(t, usage.PromptTokensDetails.CacheWriteTokensDetails)
	assert.Equal(t, 4000, *usage.PromptTokensDetails.CacheWriteTokensDetails.TTL12hTokens)
	cloned := usage.PromptTokensDetails.Clone()
	*cloned.CacheWriteTokensDetails.TTL12hTokens = 0
	assert.Equal(t, 4000, *usage.PromptTokensDetails.CacheWriteTokensDetails.TTL12hTokens, "snapshots must not share mutable counters")
	var finalSnapshot dto.Usage
	require.NoError(t, common.UnmarshalJsonStr(`{"input_tokens_details":{"cache_write_tokens_details":{"ttl_12h_tokens":0}}}`, &finalSnapshot))
	merged := dto.MergeUsageNonZero(nil, convertedBack.Usage)
	merged = dto.MergeUsageNonZero(merged, &finalSnapshot)
	assert.Equal(t, 3000, *merged.InputTokensDetails.CacheWriteTokensDetails.TTL30mTokens, "omitted lifetime counts retain the prior snapshot")
	assert.Zero(t, *merged.InputTokensDetails.CacheWriteTokensDetails.TTL12hTokens, "explicit zero replaces the prior lifetime count")
	assert.Equal(t, 4000, *convertedBack.Usage.InputTokensDetails.CacheWriteTokensDetails.TTL12hTokens, "merging must not mutate the source snapshot")

	expression, ok := billing_setting.GetBuiltinBillingExpr("gpt-6.2-sol")
	require.True(t, ok)
	snapshot := &billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000}
	params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression))
	assert.Equal(t, float64(1000), params.P)
	assert.Equal(t, float64(12000), params.Len)
	assert.Equal(t, float64(3000), params.CC)
	assert.Equal(t, float64(4000), params.CC12h)
	result, err := billingexpr.ComputeTieredQuota(snapshot, params)
	require.NoError(t, err)
	assert.Equal(t, 15650, result.ActualQuotaAfterGroup)
	other := model.NewLogOther()
	service.InjectTieredBillingInfo(other, &relaycommon.RelayInfo{TieredBillingSnapshot: snapshot}, &result)
	assert.Equal(t, float64(4000), other.Snapshot()["cache_creation_12h_tokens"])

	legacy := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(`tier("custom", p * 2 + cr * 0.2 + cc * 2.5 + c * 10)`))
	assert.Equal(t, float64(7000), legacy.CC, "existing aggregate-cache prices remain unchanged")
	assert.Zero(t, legacy.CC12h)

	for _, breakdown := range []string{
		`{"ttl_30m_tokens":3000}`, // incomplete is not the same as explicit zero
		`{"ttl_30m_tokens":3000,"ttl_12h_tokens":-1}`,
		`{"ttl_30m_tokens":3000,"ttl_12h_tokens":5000}`,
	} {
		t.Run(breakdown, func(t *testing.T) {
			invalid := usage.PromptTokensDetails.Clone()
			invalid.CacheWriteTokensDetails = nil
			require.NoError(t, common.UnmarshalJsonStr(breakdown, &invalid.CacheWriteTokensDetails))
			badUsage := *usage
			badUsage.PromptTokensDetails = invalid
			actual := service.BuildTieredTokenParams(&badUsage, false, billingexpr.UsedVars(expression))
			assert.Equal(t, float64(7000), actual.CC)
			assert.Zero(t, actual.CC12h)
		})
	}
}

func TestImageModelBuiltinPricesAndOverrides(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	for _, name := range []string{"gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare"} {
		t.Run(name, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			expression, ok := billing_setting.GetBillingExpr(name)
			require.True(t, ok)
			usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 100, PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens: 300, ImageTokens: 600, CachedTokensDetails: &dto.CachedTokenDetails{ImageTokens: common.GetPointer(200)},
			}}
			result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: 1, QuotaPerUnit: 500000},
				service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression)))
			require.NoError(t, err)
			assert.Equal(t, 4113, result.ActualQuotaAfterGroup)
			encoded, err := common.Marshal(map[string]float64{name: 0})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(encoded)))
			assert.Equal(t, "ratio", billing_setting.GetBillingMode(name))
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(encoded)))
			assert.Equal(t, "ratio", billing_setting.GetBillingMode(name))
			settings.BillingMode[name] = "tiered_expr"
			settings.BillingExpr[name] = `tier("custom", p * 7)`
			actual, ok := billing_setting.GetBillingExpr(name)
			require.True(t, ok)
			assert.Equal(t, settings.BillingExpr[name], actual)
		})
	}
}

func TestGPT56BuiltinBillingExpressions(t *testing.T) {
	for _, name := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		expression, ok := billing_setting.GetBillingExpr(name)
		require.True(t, ok)
		require.Contains(t, expression, `len <= 272000`)
		require.Contains(t, expression, `tier("standard"`)
		require.Contains(t, expression, `tier("long_context"`)
		_, err := billingexpr.CompileFromCache(expression)
		require.NoError(t, err)
	}
}
