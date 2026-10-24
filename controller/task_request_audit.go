package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

const (
	maxTaskRequestAuditBytes = 24 * 1024
	maxTaskAuditStringRunes  = 4096
)

// taskRequestAuditSnapshot produces an admin-only, bounded representation of
// the normalized task request. It deliberately omits credentials and media
// payloads while retaining prompts and ordinary generation parameters.
func taskRequestAuditSnapshot(c *gin.Context) map[string]any {
	if c == nil {
		return nil
	}
	var source any
	sourceLabel := "normalized_client_request"
	if upstreamValue, exists := c.Get("upstream_task_request_body"); exists {
		if raw, ok := upstreamValue.(string); ok {
			decoder := json.NewDecoder(bytes.NewBufferString(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&source); err != nil {
				return map[string]any{"available": false, "source": "upstream_request_body", "reason": "upstream_body_not_json"}
			}
			sourceLabel = "upstream_request_body"
		}
	}
	if source == nil {
		value, exists := c.Get("task_request")
		if !exists || value == nil {
			if omitted, present := c.Get("upstream_task_request_body_omitted"); present {
				return map[string]any{"available": false, "source": "upstream_request_body", "reason": omitted}
			}
			return nil
		}
		source = value
	}
	if request, ok := source.(relaycommon.TaskSubmitReq); ok {
		source = map[string]any{
			"prompt": request.Prompt, "model": request.Model, "mode": request.Mode,
			"size": request.Size, "duration": request.Duration, "seconds": request.Seconds,
			"resolution": request.Resolution, "image": request.Image, "images": request.Images,
			"input_reference": request.InputReference, "metadata": request.Metadata,
		}
	}
	sanitized, _ := sanitizeTaskAuditValue("", source, 0)
	encoded, err := json.Marshal(sanitized)
	if err != nil {
		return map[string]any{"available": false, "reason": "sanitized_request_not_json_serializable"}
	}
	if len(encoded) > maxTaskRequestAuditBytes {
		return map[string]any{"available": false, "reason": "sanitized_request_exceeds_audit_limit", "sanitized_bytes": len(encoded)}
	}
	return map[string]any{"available": true, "source": sourceLabel, "fields": sanitized}
}

func sanitizeTaskAuditValue(key string, value any, depth int) (any, bool) {
	if depth > 12 {
		return "[omitted: nesting limit]", true
	}
	keyLower := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
	if isTaskAuditCredentialKey(keyLower) {
		return "[redacted]", true
	}
	if isTaskAuditMediaKey(keyLower) {
		return summarizeTaskAuditMedia(value), true
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			clean, _ := sanitizeTaskAuditValue(childKey, childValue, depth+1)
			result[childKey] = clean
		}
		return result, true
	case []any:
		result := make([]any, 0, len(typed))
		for _, childValue := range typed {
			clean, _ := sanitizeTaskAuditValue(key, childValue, depth+1)
			result = append(result, clean)
		}
		return result, true
	case string:
		return sanitizeTaskAuditString(typed), true
	default:
		return value, true
	}
}

func isTaskAuditCredentialKey(key string) bool {
	for _, fragment := range []string{"key", "authorization", "bearer", "secret", "password", "token"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

func isTaskAuditMediaKey(key string) bool {
	switch key {
	case "image", "images", "file", "files", "mask", "inlineData", "inlinedata", "filedata":
		return true
	}
	if strings.Contains(key, "inputreference") || strings.Contains(key, "referenceimage") || strings.HasSuffix(key, "images") {
		return true
	}
	if strings.Contains(key, "image") || strings.Contains(key, "video") || strings.Contains(key, "audio") || strings.Contains(key, "file") {
		for _, sensitivePart := range []string{"url", "data", "base64", "content", "payload", "file"} {
			if strings.Contains(key, sensitivePart) {
				return true
			}
		}
	}
	return false
}

func summarizeTaskAuditMedia(value any) any {
	switch typed := value.(type) {
	case []any:
		return map[string]any{"redacted": true, "count": len(typed)}
	case string:
		return map[string]any{"redacted": true, "provided": typed != ""}
	case nil:
		return map[string]any{"redacted": true, "provided": false}
	default:
		return map[string]any{"redacted": true, "provided": true}
	}
}

func sanitizeTaskAuditString(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return fmt.Sprintf("[redacted data URI: %d bytes]", len(value))
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "http://") || strings.HasPrefix(strings.ToLower(trimmed), "https://") {
		if parsed, err := url.Parse(trimmed); err == nil {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.Fragment = ""
			return parsed.String()
		}
	}
	if len(value) > 8192 && looksLikeBase64(value) {
		return fmt.Sprintf("[redacted binary-like value: %d bytes]", len(value))
	}
	if utf8.RuneCountInString(value) > maxTaskAuditStringRunes {
		runes := []rune(value)
		return string(runes[:maxTaskAuditStringRunes]) + "…[truncated]"
	}
	return value
}

func looksLikeBase64(value string) bool {
	if len(value) == 0 {
		return false
	}
	allowed := 0
	for _, r := range value {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '=' || r == '-' || r == '_' {
			allowed++
		}
	}
	return float64(allowed)/float64(len(value)) > 0.98
}
