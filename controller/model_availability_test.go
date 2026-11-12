package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/stretchr/testify/require"
)

func TestBuildModelAvailabilityUsesExactMetricsAndSafeStates(t *testing.T) {
	pricing := []model.Pricing{
		{
			ModelName:              "availability-model",
			EnableGroup:            []string{"nano-banana"},
			SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeImageGeneration},
		},
	}
	query := func(string) (perfmetrics.QueryResult, error) {
		return perfmetrics.QueryResult{
			Groups: []perfmetrics.GroupResult{
				{Group: "nano-banana", RequestCount: 3, SuccessCount: 2, AvgLatencyMs: 1250},
			},
		}, nil
	}

	result, err := buildModelAvailability(pricing, map[string]string{"nano-banana": "1"}, query)
	require.NoError(t, err)
	modelResult, ok := result["availability-model"]
	require.True(t, ok)
	require.Equal(t, "image", modelResult.Kind)
	require.Len(t, modelResult.Groups, 1)
	require.Equal(t, "partial", modelResult.Groups[0].ServiceState)
	require.EqualValues(t, 3, modelResult.Groups[0].Recent.Total)
	require.EqualValues(t, 2, modelResult.Groups[0].Recent.SuccessCount)
	require.EqualValues(t, 1, modelResult.Groups[0].Recent.FailureCount)
	require.NotNil(t, modelResult.Groups[0].Recent.AvgDurationMS)
}
