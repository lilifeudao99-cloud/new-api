package controller

import (
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// Performance statistics describe historical traffic, not live channel admission.
// Unknown live state and unavailable timings must never be fabricated as healthy.
type modelAvailabilityRow struct {
	Group        string `json:"group,omitempty"`
	ServiceState string `json:"service_state"`
	Recent       struct {
		Total         int64    `json:"total"`
		SuccessCount  int64    `json:"success_count"`
		FailureCount  int64    `json:"failure_count"`
		AvgDurationMS *float64 `json:"avg_duration_ms"`
		AvgResponseMS *float64 `json:"avg_response_ms"`
		AvgTPS        *float64 `json:"avg_tps"`
		WindowSeconds int      `json:"window_seconds"`
	} `json:"recent"`
}

type modelAvailability struct {
	Kind    string                 `json:"kind"`
	Summary modelAvailabilityRow   `json:"summary"`
	Groups  []modelAvailabilityRow `json:"groups"`
}

type availabilityMetricCache struct {
	sync.Mutex
	expiresAt time.Time
	result    perfmetrics.QueryResult
}

var availabilityMetricsCache = struct {
	sync.Mutex
	entries map[string]*availabilityMetricCache
}{entries: map[string]*availabilityMetricCache{}}

func queryAvailabilityMetrics(name string) (perfmetrics.QueryResult, error) {
	availabilityMetricsCache.Lock()
	entry := availabilityMetricsCache.entries[name]
	if entry == nil {
		// Bound memory even when model names are frequently changed by admins.
		if len(availabilityMetricsCache.entries) >= 1024 {
			clear(availabilityMetricsCache.entries)
		}
		entry = &availabilityMetricCache{}
		availabilityMetricsCache.entries[name] = entry
	}
	availabilityMetricsCache.Unlock()
	entry.Lock()
	defer entry.Unlock()
	if time.Now().Before(entry.expiresAt) {
		return entry.result, nil
	}
	result, err := perfmetrics.Query(perfmetrics.QueryParams{Model: name, Hours: 1})
	if err != nil {
		return perfmetrics.QueryResult{}, err
	}
	entry.result = result
	entry.expiresAt = time.Now().Add(20 * time.Second)
	return result, nil
}

func GetModelAvailability(c *gin.Context) {
	// Apply this to failures too: usable groups depend on the authenticated user.
	c.Header("Cache-Control", "private, no-store")
	group := ""
	if id, ok := c.Get("id"); ok {
		user, err := model.GetUserCache(id.(int))
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Model status temporarily unavailable"})
			return
		}
		group = user.Group
	}
	usable := service.GetUserUsableGroups(group)
	pricing := filterPricingByUsableGroups(model.GetPricing(), usable)
	observed := time.Now().Unix()
	models, err := buildModelAvailability(pricing, usable, queryAvailabilityMetrics)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Model status temporarily unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "observed_at": observed, "expires_at": observed + 30, "models": models})
}

func buildModelAvailability(pricing []model.Pricing, usable map[string]string, query func(string) (perfmetrics.QueryResult, error)) (map[string]modelAvailability, error) {
	result := make(map[string]modelAvailability, len(pricing))
	for _, price := range pricing {
		item := modelAvailability{Kind: "llm", Summary: modelAvailabilityRow{ServiceState: "unknown"}, Groups: []modelAvailabilityRow{}}
		for _, endpoint := range price.SupportedEndpointTypes {
			switch endpoint {
			case constant.EndpointTypeImageGeneration:
				item.Kind = "image"
			case constant.EndpointTypeOpenAIVideo:
				item.Kind = "video"
			}
		}
		groups := make([]string, 0, len(price.EnableGroup))
		if slices.Contains(price.EnableGroup, "all") {
			for group := range usable {
				groups = append(groups, group)
			}
		} else {
			for _, group := range price.EnableGroup {
				if _, ok := usable[group]; ok {
					groups = append(groups, group)
				}
			}
		}
		if len(groups) == 0 {
			continue
		}
		sort.Strings(groups)
		groups = slices.Compact(groups)
		metrics, err := query(price.ModelName)
		if err != nil {
			return nil, err
		}
		metricsByGroup := make(map[string]perfmetrics.GroupResult, len(metrics.Groups))
		for _, metric := range metrics.Groups {
			metricsByGroup[metric.Group] = metric
		}
		for _, group := range groups {
			metric := metricsByGroup[group]
			row := modelAvailabilityRow{Group: group, ServiceState: "unknown"}
			row.Recent.Total = metric.RequestCount
			row.Recent.SuccessCount = metric.SuccessCount
			row.Recent.FailureCount = metric.RequestCount - metric.SuccessCount
			row.Recent.WindowSeconds = 3600
			if metric.RequestCount > 0 && metric.AvgLatencyMs > 0 {
				value := float64(metric.AvgLatencyMs)
				row.Recent.AvgDurationMS = &value
			}
			if metric.AvgTtftMs > 0 {
				value := float64(metric.AvgTtftMs)
				row.Recent.AvgResponseMS = &value
			}
			if item.Kind == "llm" && metric.AvgTps > 0 {
				value := metric.AvgTps
				row.Recent.AvgTPS = &value
			}
			item.Groups = append(item.Groups, row)
			item.Summary.Recent.Total += row.Recent.Total
			item.Summary.Recent.SuccessCount += row.Recent.SuccessCount
			item.Summary.Recent.FailureCount += row.Recent.FailureCount
		}
		item.Summary.Recent.WindowSeconds = 3600
		result[price.ModelName] = item
	}
	return result, nil
}
