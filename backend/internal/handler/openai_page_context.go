package handler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

var codexSelectedPageName = regexp.MustCompile(`^codex_apps_selected_page_part_([1-9][0-9]*)_of_([1-9][0-9]*)$`)

// Codex sends external context without a model tool call. Use its legacy text
// representation for HTTP interoperability; this does not preserve native trust semantics.
func normalizeCodexUntrustedContext(body []byte) ([]byte, bool) {
	if !mayContainCodexUntrustedContext(body) || !hasUniqueJSONMembers(body) {
		return body, false
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return body, false
	}
	var input []json.RawMessage
	if json.Unmarshal(request["input"], &input) != nil {
		return body, false
	}
	changed := false
	for i, raw := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		readString := func(key string) string {
			var value string
			_ = json.Unmarshal(item[key], &value)
			return value
		}
		if readString("type") != "function_call_output" || readString("namespace") != "agent" {
			continue
		}
		if rawCallID, exists := item["call_id"]; exists {
			var callID *string
			if json.Unmarshal(rawCallID, &callID) != nil || callID == nil || strings.TrimSpace(*callID) != "" {
				continue
			}
		}
		name := readString("name")
		var output *string
		if json.Unmarshal(item["output"], &output) != nil || output == nil || !validCodexUntrustedContext(name, *output) {
			continue
		}
		text := "<external_" + name + ">" + *output + "</external_" + name + ">"
		if name == "codex_apps_open_page" || strings.HasPrefix(name, "codex_apps_selected_page_part_") {
			text = "<external_" + name + ">\n" + *output + "\n</external_" + name + ">"
		}
		message := map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]string{{
				"type": "input_text",
				"text": text,
			}},
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return body, false
		}
		input[i] = encoded
		changed = true
	}
	if !changed {
		return body, false
	}
	encodedInput, err := json.Marshal(input)
	if err != nil {
		return body, false
	}
	request["input"] = encodedInput
	encoded, err := json.Marshal(request)
	if err != nil {
		return body, false
	}
	return encoded, true
}

func mayContainCodexUntrustedContext(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() != "function_call_output" || item.Get("namespace").String() != "agent" {
			return true
		}
		name := item.Get("name").String()
		switch name {
		case "codex_apps_open_page", "browser_info", "browser_tabs", "goal_objective", "untrusted_context",
			"untrusted_content", "ide_context", "proposed_plan", "terminal_info":
			found = true
		default:
			found = strings.HasPrefix(name, "codex_apps_selected_page_part_")
		}
		return !found
	})
	return found
}

func validCodexUntrustedContext(name, output string) bool {
	switch name {
	case "browser_info", "goal_objective", "untrusted_context", "ide_context", "terminal_info":
		return strings.TrimSpace(output) != ""
	case "browser_tabs":
		payload, opened := strings.CutPrefix(strings.TrimSpace(output), `<in-app-browser-context source="ambient-ui-state">`)
		payload, closed := strings.CutSuffix(payload, "</in-app-browser-context>")
		return opened && closed && strings.TrimSpace(payload) != ""
	case "untrusted_content":
		// Codex's unnamed context source can carry an empty string.
		return true
	case "proposed_plan":
		// Approved plans are split by byte length, so a chunk can be whitespace.
		return output != ""
	default:
		return validCodexPageContext(name, output)
	}
}

func validCodexPageContext(name, output string) bool {
	if name == "codex_apps_open_page" {
		return validCodexPageSnapshot(output)
	}
	parts := codexSelectedPageName.FindStringSubmatch(name)
	if len(parts) != 3 {
		return false
	}
	part, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	total, err := strconv.Atoi(parts[2])
	if err != nil || part > total {
		return false
	}
	header, snapshot, ok := strings.Cut(output, "\n")
	if !ok || strings.TrimSuffix(header, "\r") != fmt.Sprintf("Companion Page snapshot part %d of %d:", part, total) || strings.TrimSpace(snapshot) == "" {
		return false
	}
	// A multipart snapshot can split inside a JSON string; keep each chunk verbatim.
	return total > 1 || validCodexPageSnapshot(snapshot)
}

func validCodexPageSnapshot(text string) bool {
	if !hasUniqueJSONMembers([]byte(text)) {
		return false
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &snapshot) != nil {
		return false
	}
	rawID, exists := snapshot["page_id"]
	if !exists {
		return false
	}
	var id *string
	return json.Unmarshal(rawID, &id) == nil && (id == nil || strings.TrimSpace(*id) != "")
}
