package common

import (
	"strconv"
	"strings"
)

const (
	GrokVideoModel              = "grok-imagine-video"
	GrokVideoBillingModelPrefix = GrokVideoModel + "-billing-"
)

// IsSoraVideoModel identifies the only models that use the Sora task plugin
// on the shared OpenAI Video endpoint. All other video models use the legacy
// OpenAI-compatible task adaptor.
func IsSoraVideoModel(model string) bool {
	switch strings.TrimSpace(model) {
	case "sora-2", "sora-2-pro":
		return true
	default:
		return false
	}
}

var grokVideoBillingModels = map[string]string{
	"480p":  GrokVideoBillingModelPrefix + "480p",
	"720p":  GrokVideoBillingModelPrefix + "720p",
	"1080p": GrokVideoBillingModelPrefix + "1080p",
}

// GrokVideoBillingModel returns the internal billing model for a resolution.
// Unknown or missing resolutions intentionally use the existing 1080p
// compatibility default.
func GrokVideoBillingModel(resolution string) string {
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if model, ok := grokVideoBillingModels[resolution]; ok {
		return model
	}
	return grokVideoBillingModels["1080p"]
}

// IsInternalVideoBillingModel identifies model ids that are implementation
// details and must not be shown by the public model list.
func IsInternalVideoBillingModel(model string) bool {
	return strings.HasPrefix(model, GrokVideoBillingModelPrefix)
}

// NormalizeVideoRequestMap applies the compatibility transformations used by
// the former video-request-normalizer sidecar. The seconds field remains a
// string for compatibility with legacy task request decoding, while a parsed
// numeric value is mirrored to duration so task plugins that consume either
// spelling see the same value.
func NormalizeVideoRequestMap(request map[string]any) bool {
	if request == nil {
		return false
	}

	changed := false
	model, _ := request["model"].(string)
	if model == GrokVideoModel || IsInternalVideoBillingModel(model) {
		resolution, _ := request["resolution"].(string)
		resolution = strings.ToLower(strings.TrimSpace(resolution))
		if _, ok := grokVideoBillingModels[resolution]; !ok {
			resolution = "1080p"
		}
		if request["resolution"] != resolution {
			request["resolution"] = resolution
			changed = true
		}
		billingModel := GrokVideoBillingModel(resolution)
		if request["model"] != billingModel {
			request["model"] = billingModel
			changed = true
		}
	}

	if seconds, ok := request["seconds"].(string); ok {
		rawSeconds := seconds
		seconds = strings.TrimSpace(strings.ToLower(seconds))
		seconds, _ = strings.CutSuffix(seconds, "s")
		seconds = strings.TrimSpace(seconds)
		if duration, err := strconv.Atoi(seconds); err == nil && duration > 0 {
			canonicalSeconds := strconv.Itoa(duration)
			if rawSeconds != canonicalSeconds {
				request["seconds"] = canonicalSeconds
				changed = true
			}
			if _, exists := request["duration"]; !exists {
				request["duration"] = duration
				changed = true
			}
		}
	}

	return changed
}

// NormalizeVideoRequestBody decodes and rewrites a JSON video request. It
// returns the original bytes when no compatibility change is needed.
func NormalizeVideoRequestBody(body []byte) ([]byte, bool, error) {
	var request map[string]any
	if err := Unmarshal(body, &request); err != nil {
		return nil, false, err
	}
	if !NormalizeVideoRequestMap(request) {
		return body, false, nil
	}
	normalized, err := Marshal(request)
	if err != nil {
		return nil, false, err
	}
	return normalized, true, nil
}
