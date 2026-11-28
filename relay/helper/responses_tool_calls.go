package helper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// NormalizeResponsesToolCallInput preserves canonical call IDs and only repairs
// an output when the client supplied an explicit, unambiguous association. A
// response ID, output item ID or position is never used as a fabricated call ID.
func NormalizeResponsesToolCallInput(input json.RawMessage) (json.RawMessage, error) {
	if common.GetJsonType(input) != "array" {
		return input, nil
	}
	var items []map[string]json.RawMessage
	if err := common.Unmarshal(input, &items); err != nil {
		return nil, fmt.Errorf("invalid Responses input: expected an array of objects")
	}
	callIDs := make(map[string]string)
	ambiguous := make(map[string]bool)
	for _, item := range items {
		var kind, id, callID string
		_ = common.Unmarshal(item["type"], &kind)
		_ = common.Unmarshal(item["id"], &id)
		_ = common.Unmarshal(item["call_id"], &callID)
		if kind != "function_call" || id == "" || strings.TrimSpace(callID) == "" {
			continue
		}
		if previous, ok := callIDs[id]; ok && previous != callID {
			ambiguous[id] = true
		}
		callIDs[id] = callID
	}
	changed := false
	for i, item := range items {
		var kind string
		_ = common.Unmarshal(item["type"], &kind)
		if kind != "function_call_output" {
			continue
		}
		var callID string
		if raw := bytes.TrimSpace(item["call_id"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			if err := common.Unmarshal(raw, &callID); err != nil {
				return nil, fmt.Errorf("input[%d].call_id must be a string", i)
			}
		}
		if callID != "" {
			if strings.TrimSpace(callID) == "" {
				return nil, fmt.Errorf("input[%d].call_id must not be blank", i)
			}
			// Compatibility-only aliases cannot invalidate a canonical call ID.
			// Preserve its exact value and the original request bytes.
			continue
		}
		var toolCallID, itemID string
		for _, field := range []struct {
			name  string
			value *string
		}{{"tool_call_id", &toolCallID}, {"item_id", &itemID}} {
			if raw, ok := item[field.name]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				if err := common.Unmarshal(raw, field.value); err != nil {
					return nil, fmt.Errorf("input[%d].%s must be a string", i, field.name)
				}
			}
		}
		resolved := toolCallID
		if itemID != "" {
			mapped := callIDs[itemID]
			if ambiguous[itemID] || mapped == "" || resolved != "" && resolved != mapped {
				return nil, fmt.Errorf("input[%d] has an unresolved or conflicting tool call reference; provide the original function_call.call_id", i)
			}
			resolved = mapped
		}
		if strings.TrimSpace(resolved) == "" {
			return nil, fmt.Errorf("input[%d].call_id is required for function_call_output; 请回传对应 function_call.call_id，previous_response_id 不能替代工具调用 ID", i)
		}
		encoded, err := common.Marshal(resolved)
		if err != nil {
			return nil, err
		}
		item["call_id"] = encoded
		delete(item, "tool_call_id")
		delete(item, "item_id")
		changed = true
	}
	if !changed {
		return input, nil
	}
	return common.Marshal(items)
}

// ResponsesToolCallInputDiagnostics reports only fixed field names and states.
// Never include IDs, arguments, outputs, arbitrary keys or other request values.
// Samples are bounded even for long, malformed conversation histories.
func ResponsesToolCallInputDiagnostics(input json.RawMessage) map[string]any {
	var items []map[string]json.RawMessage
	if err := common.Unmarshal(input, &items); err != nil {
		return map[string]any{"input_array": false}
	}
	var calls, outputs, missing int
	samples := make([]map[string]any, 0)
	for i, item := range items {
		var kind string
		_ = common.Unmarshal(item["type"], &kind)
		if kind == "function_call" {
			calls++
		}
		if kind != "function_call_output" {
			continue
		}
		outputs++
		states := make(map[string]string)
		for _, name := range []string{"call_id", "tool_call_id", "item_id", "id"} {
			raw, exists := item[name]
			state := "missing"
			if exists {
				var value string
				switch {
				case bytes.Equal(bytes.TrimSpace(raw), []byte("null")):
					state = "null"
				case common.Unmarshal(raw, &value) != nil:
					state = "non_string"
				case strings.TrimSpace(value) == "":
					state = "blank"
				default:
					state = "present"
				}
			}
			states[name] = state
		}
		if states["call_id"] == "present" {
			continue
		}
		missing++
		if len(samples) < 8 {
			samples = append(samples, map[string]any{"index": i, "fields": states})
		}
	}
	return map[string]any{"input_array": true, "input_items": len(items), "function_calls": calls, "function_outputs": outputs, "outputs_without_call_id": missing, "samples": samples, "samples_truncated": missing > len(samples)}
}

// NormalizeResponsesToolCallBody also covers passthrough and parameter overrides
// without discarding unknown top-level fields or previous_response_id.
func NormalizeResponsesToolCallBody(body []byte) ([]byte, error) {
	var request map[string]json.RawMessage
	if err := common.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	input := request["input"]
	normalized, err := NormalizeResponsesToolCallInput(input)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(input, normalized) {
		return body, nil
	}
	request["input"] = normalized
	return common.Marshal(request)
}
