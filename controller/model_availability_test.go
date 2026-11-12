package controller

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildModelAvailability(t *testing.T) {
	for _, tc := range []struct {
		name     string
		total    int64
		success  int64
		endpoint constant.EndpointType
		kind     string
	}{
		{"no traffic", 0, 0, constant.EndpointTypeOpenAI, "llm"},
		{"successful traffic", 3, 3, constant.EndpointTypeImageGeneration, "image"},
		{"mixed traffic", 3, 2, constant.EndpointTypeOpenAI, "llm"},
		{"failed traffic", 3, 0, constant.EndpointTypeOpenAIVideo, "video"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pricing := []model.Pricing{{ModelName: "visible", EnableGroup: []string{"public", "public", "private", "empty"}, SupportedEndpointTypes: []constant.EndpointType{tc.endpoint}}, {ModelName: "hidden", EnableGroup: []string{"private"}}}
			queries := 0
			result, err := buildModelAvailability(pricing, map[string]string{"public": "Public", "empty": "Empty"}, func(name string) (perfmetrics.QueryResult, error) {
				queries++
				assert.Equal(t, "visible", name)
				return perfmetrics.QueryResult{Groups: []perfmetrics.GroupResult{
					{Group: "public", RequestCount: tc.total, SuccessCount: tc.success, AvgLatencyMs: 1250},
					{Group: "private", RequestCount: 999, SuccessCount: 999},
				}}, nil
			})
			require.NoError(t, err)
			require.Len(t, result, 1)
			assert.Equal(t, 1, queries)
			item := result["visible"]
			assert.Equal(t, tc.kind, item.Kind)
			assert.Equal(t, "unknown", item.Summary.ServiceState, "historical success does not prove live admission")
			assert.Equal(t, tc.total, item.Summary.Recent.Total)
			assert.Equal(t, tc.success, item.Summary.Recent.SuccessCount)
			assert.Equal(t, tc.total-tc.success, item.Summary.Recent.FailureCount)
			require.Len(t, item.Groups, 2)
			assert.Equal(t, "empty", item.Groups[0].Group)
			assert.Zero(t, item.Groups[0].Recent.Total)
			assert.Nil(t, item.Groups[0].Recent.AvgDurationMS)
			assert.Equal(t, "public", item.Groups[1].Group)
			assert.Equal(t, "unknown", item.Groups[1].ServiceState)
			if tc.total > 0 {
				require.NotNil(t, item.Groups[1].Recent.AvgDurationMS)
				assert.Equal(t, float64(1250), *item.Groups[1].Recent.AvgDurationMS)
			} else {
				assert.Nil(t, item.Groups[1].Recent.AvgDurationMS)
			}
			encoded, err := common.Marshal(result)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "private")
		})
	}
}

func TestBuildModelAvailabilityFailureAndWildcardGroups(t *testing.T) {
	pricing := []model.Pricing{{ModelName: "model", EnableGroup: []string{"all"}}}
	result, err := buildModelAvailability(pricing, map[string]string{"public": "Public"}, func(string) (perfmetrics.QueryResult, error) {
		return perfmetrics.QueryResult{}, errors.New("private database diagnostics")
	})
	require.Error(t, err)
	assert.Nil(t, result, "a failed query must not look like an empty successful snapshot")
	result, err = buildModelAvailability(pricing, map[string]string{"public": "Public"}, func(string) (perfmetrics.QueryResult, error) {
		return perfmetrics.QueryResult{}, nil
	})
	require.NoError(t, err)
	require.Len(t, result["model"].Groups, 1)
	assert.Equal(t, "public", result["model"].Groups[0].Group)
	assert.Equal(t, "unknown", result["model"].Groups[0].ServiceState)
}
