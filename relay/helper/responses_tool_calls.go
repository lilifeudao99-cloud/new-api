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
		var callID, toolCallID, itemID string
		for _, field := range []struct {
			name  string
			value *string
		}{{"call_id", &callID}, {"tool_call_id", &toolCallID}, {"item_id", &itemID}} {
			if raw, ok := item[field.name]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				if err := common.Unmarshal(raw, field.value); err != nil {
					return nil, fmt.Errorf("input[%d].%s must be a string", i, field.name)
				}
			}
		}
		if callID != "" {
			if strings.TrimSpace(callID) == "" {
				return nil, fmt.Errorf("input[%d].call_id must not be blank", i)
			}
			// Canonical requests retain their original fields and bytes.
			continue
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
