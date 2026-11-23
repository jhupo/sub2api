package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const codexBrowserTabsOutput = "\n<in-app-browser-context source=\"ambient-ui-state\">\n# In app browser\n- Example tab: https://example.com/\n</in-app-browser-context>\n"

var codexTextContextNamesForTest = []string{
	"browser_info", "goal_objective", "untrusted_context", "ide_context",
	"untrusted_content", "proposed_plan", "terminal_info",
}

func pageContextItem(name, output string) map[string]any {
	return map[string]any{
		"type": "function_call_output", "id": "fco_context",
		"namespace": "agent", "name": name, "output": output,
	}
}

func pageContextRequest(t *testing.T, items ...any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "gpt-5", "input": items})
	require.NoError(t, err)
	return body
}

func TestNormalizeCodexUntrustedContextFirstTurn(t *testing.T) {
	cases := []struct {
		name   string
		output string
		page   bool
	}{
		{name: "codex_apps_open_page", output: `{"page_id":null,"document_view":null}`, page: true},
		{name: "codex_apps_selected_page_part_1_of_1", output: "Companion Page snapshot part 1 of 1:\r\n" + `{"page_id":"page_1"}`, page: true},
		{name: "browser_tabs", output: codexBrowserTabsOutput},
	}
	for _, name := range codexTextContextNamesForTest {
		cases = append(cases, struct {
			name   string
			output string
			page   bool
		}{name: name, output: "external context\nline two"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := pageContextRequest(t, pageContextItem(tc.name, tc.output),
				map[string]any{"role": "user", "content": "continue"})
			h := &OpenAIGatewayHandler{}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			require.False(t, h.validateFunctionCallOutputRequest(c, body, zap.NewNop()))
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), "function_call_output requires call_id")

			got, changed := normalizeCodexUntrustedContext(body)
			require.True(t, changed)
			w = httptest.NewRecorder()
			c, _ = gin.CreateTestContext(w)
			require.True(t, h.validateFunctionCallOutputRequest(c, got, zap.NewNop()), w.Body.String())
			require.False(t, c.Writer.Written())
			item := gjson.GetBytes(got, "input.0")
			require.Equal(t, "message", item.Get("type").String())
			require.Equal(t, "user", item.Get("role").String())
			wantText := "<external_" + tc.name + ">" + tc.output + "</external_" + tc.name + ">"
			if tc.page {
				wantText = "<external_" + tc.name + ">\n" + tc.output + "\n</external_" + tc.name + ">"
			}
			require.Equal(t, wantText, item.Get("content.0.text").String())
			for _, field := range []string{"id", "call_id", "name", "namespace", "output"} {
				require.False(t, item.Get(field).Exists(), field)
			}
			require.Equal(t, "continue", gjson.GetBytes(got, "input.1.content").String())
			again, changedAgain := normalizeCodexUntrustedContext(got)
			require.False(t, changedAgain)
			require.Equal(t, got, again)
		})
	}
}

func TestNormalizeCodexUntrustedContextBlankText(t *testing.T) {
	for _, name := range codexTextContextNamesForTest {
		for _, output := range []string{"", " \t\r\n"} {
			t.Run(fmt.Sprintf("%s/%q", name, output), func(t *testing.T) {
				body := pageContextRequest(t, pageContextItem(name, output))
				got, changed := normalizeCodexUntrustedContext(body)
				wantChange := name == "untrusted_content" || (name == "proposed_plan" && output != "")
				require.Equal(t, wantChange, changed)
				if wantChange {
					require.Equal(t, "<external_"+name+">"+output+"</external_"+name+">",
						gjson.GetBytes(got, "input.0.content.0.text").String())
				} else {
					require.Equal(t, body, got)
				}
			})
		}
	}
}

func TestNormalizeCodexUntrustedContextBlankCallIDs(t *testing.T) {
	for _, callID := range []string{"", " \t\n"} {
		items := make([]any, 0, len(codexTextContextNamesForTest))
		for _, name := range codexTextContextNamesForTest {
			item := pageContextItem(name, "context for "+name)
			item["call_id"] = callID
			delete(item, "id")
			items = append(items, item)
		}
		got, changed := normalizeCodexUntrustedContext(pageContextRequest(t, items...))
		require.True(t, changed)
		messages := gjson.GetBytes(got, "input").Array()
		require.Len(t, messages, len(items))
		for i, item := range messages {
			require.Equal(t, "user", item.Get("role").String())
			name := codexTextContextNamesForTest[i]
			require.Equal(t, "<external_"+name+">context for "+name+"</external_"+name+">",
				item.Get("content.0.text").String())
		}
	}
}

func TestNormalizeCodexUntrustedContextPreservesHistory(t *testing.T) {
	body := []byte(`{"model":"gpt-5","previous_response_id":"resp_history","metadata":{"large":9007199254740993,"fraction":0.1234567890123456789},"input":[{"role":"user","content":"start"},{"type":"function_call","name":"read_file","call_id":"call_real","arguments":"{}"},{"type":"function_call_output","call_id":"call_real","output":"result"},{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"C:\\project"},{"type":"reasoning","id":"rs_1","encrypted_content":"sealed","summary":[]},{"role":"user","content":"continue"}]}`)
	got, changed := normalizeCodexUntrustedContext(body)
	require.True(t, changed)
	require.Len(t, gjson.GetBytes(got, "input").Array(), 6)
	for _, path := range []string{"model", "previous_response_id", "metadata", "input.0", "input.1", "input.2", "input.4", "input.5"} {
		require.JSONEq(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(got, path).Raw, path)
	}
	require.Equal(t, "9007199254740993", gjson.GetBytes(got, "metadata.large").Raw)
	require.Equal(t, "0.1234567890123456789", gjson.GetBytes(got, "metadata.fraction").Raw)
	require.Equal(t, "<external_terminal_info>C:\\project</external_terminal_info>", gjson.GetBytes(got, "input.3.content.0.text").String())
}

func TestNormalizeCodexUntrustedContextPreservesRealAndMalformedOutputs(t *testing.T) {
	for _, name := range codexTextContextNamesForTest {
		for _, tc := range []struct {
			name  string
			key   string
			value any
		}{
			{name: "real call", key: "call_id", value: "call_real"},
			{name: "null call", key: "call_id", value: nil},
			{name: "numeric call", key: "call_id", value: 42},
			{name: "object call", key: "call_id", value: map[string]any{}},
			{name: "other namespace", key: "namespace", value: "other"},
			{name: "null namespace", key: "namespace", value: nil},
			{name: "numeric namespace", key: "namespace", value: 42},
			{name: "other type", key: "type", value: "custom_tool_call_output"},
			{name: "null output", key: "output", value: nil},
			{name: "numeric output", key: "output", value: 42},
			{name: "array output", key: "output", value: []any{map[string]any{"type": "encrypted_content", "encrypted_content": "sealed"}}},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				item := pageContextItem(name, "context")
				item[tc.key] = tc.value
				body := pageContextRequest(t, item)
				got, changed := normalizeCodexUntrustedContext(body)
				require.False(t, changed)
				require.Equal(t, body, got)
			})
		}
		for _, field := range []string{"type", "namespace", "name", "output"} {
			item := pageContextItem(name, "context")
			delete(item, field)
			body := pageContextRequest(t, item)
			got, changed := normalizeCodexUntrustedContext(body)
			require.False(t, changed, "name=%s missing=%s", name, field)
			require.Equal(t, body, got)
		}
	}
}

func TestNormalizeCodexUntrustedContextDoesNotValidateUnknownTools(t *testing.T) {
	for _, tool := range []map[string]any{
		pageContextItem("read_file", "tool result"),
		{"type": "function_call_output", "call_id": "call_unpaired", "output": "tool result"},
	} {
		body := pageContextRequest(t, pageContextItem("terminal_info", "terminal context"), tool)
		got, changed := normalizeCodexUntrustedContext(body)
		require.True(t, changed)
		require.JSONEq(t, gjson.GetBytes(body, "input.1").Raw, gjson.GetBytes(got, "input.1").Raw)
		h := &OpenAIGatewayHandler{}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		require.False(t, h.validateFunctionCallOutputRequest(c, got, zap.NewNop()))
		require.Equal(t, http.StatusBadRequest, w.Code)
	}
}

func TestNormalizeCodexUntrustedContextEscapedNames(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[{"type":"function_call\u005foutput","namespace":"ag\u0065nt","na\u006de":"terminal\u005finfo","output":"working directory"}]}`)
	got, changed := normalizeCodexUntrustedContext(body)
	require.True(t, changed)
	require.Equal(t, "<external_terminal_info>working directory</external_terminal_info>", gjson.GetBytes(got, "input.0.content.0.text").String())
}

func TestNormalizeCodexUntrustedContextRejectsAmbiguousJSON(t *testing.T) {
	valid := `{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"context"}`
	for _, body := range []string{
		`{"input":[` + valid + `],"model":"first","model":"second"}`,
		`{"input":[` + valid + `],"metadata":{"key":1,"key":2}}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"terminal_info","call_id":"real","call_id":"","output":"context"}]}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"terminal_info","output":"first","output":"second"}]}`,
		`{"input":[{"type":"function_call_output","namespace":"agent","name":"read_file","na\u006de":"terminal_info","output":"context"}]}`,
		`{"input":[` + valid + `]} trailing`,
		`{"input":[` + valid + `]} {"another":true}`,
		`{"input":[` + valid + `]`,
		`{"input":null}`,
		`{"input":{"name":"terminal_info"}}`,
		`{"input":"terminal_info"}`,
		`{"input":[null,42,"terminal_info"]}`,
		`{"model":"gpt-5"}`,
		`null`,
		`[]`,
	} {
		got, changed := normalizeCodexUntrustedContext([]byte(body))
		require.False(t, changed, body)
		require.Equal(t, body, string(got))
	}
}

func TestNormalizeCodexPageContextMultipart(t *testing.T) {
	first := "Companion Page snapshot part 1 of 2:\n" + `{"page_id":"page_1","document_view":"hello`
	second := "Companion Page snapshot part 2 of 2:\n" + ` world"}`
	body := pageContextRequest(t,
		pageContextItem("codex_apps_selected_page_part_1_of_2", first),
		pageContextItem("codex_apps_selected_page_part_2_of_2", second))
	got, changed := normalizeCodexUntrustedContext(body)
	require.True(t, changed)
	for i, text := range []string{first, second} {
		name := fmt.Sprintf("codex_apps_selected_page_part_%d_of_2", i+1)
		require.Equal(t, "<external_"+name+">\n"+text+"\n</external_"+name+">",
			gjson.GetBytes(got, fmt.Sprintf("input.%d.content.0.text", i)).String())
	}
}

func TestNormalizeCodexPageContextRejectsMalformedSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
	}{
		{name: "codex_apps_open_page", output: "not a page snapshot"},
		{name: "codex_apps_open_page", output: `{"other":null}`},
		{name: "codex_apps_open_page", output: `{"page_id":42}`},
		{name: "codex_apps_open_page", output: `{"page_id":" "}`},
		{name: "codex_apps_open_page", output: `{"page_id":null,"page_id":"other"}`},
		{name: "codex_apps_selected_page_part_0_of_1", output: "Companion Page snapshot part 0 of 1:\n{}"},
		{name: "codex_apps_selected_page_part_2_of_1", output: "Companion Page snapshot part 2 of 1:\n{}"},
		{name: "codex_apps_selected_page_part_1_of_1", output: "Companion Page snapshot part 1 of 1:\nnot JSON"},
		{name: "codex_apps_selected_page_part_1_of_2", output: "Companion Page snapshot part 2 of 2:\nchunk"},
		{name: "codex_apps_selected_page_part_1_of_2", output: "Companion Page snapshot part 1 of 2:\n \t"},
		{name: "codex_apps_selected_page_part_1_of_2", output: "missing header"},
		{name: "codex_apps_selected_page_part_999999999999999999999_of_2", output: "chunk"},
	} {
		body := pageContextRequest(t, pageContextItem(tc.name, tc.output))
		got, changed := normalizeCodexUntrustedContext(body)
		require.False(t, changed, "name=%s output=%q", tc.name, tc.output)
		require.Equal(t, body, got)
	}
}

func TestNormalizeCodexBrowserTabsRejectsMalformedWrappers(t *testing.T) {
	for _, output := range []string{
		"ordinary tool result", "", " \t",
		`<in-app-browser-context source="ambient-ui-state"> </in-app-browser-context>`,
		`<in-app-browser-context source="other">tab</in-app-browser-context>`,
		`<in-app-browser-context source="ambient-ui-state">tab`,
		`<in-app-browser-context source="ambient-ui-state">tab</in-app-browser-context>suffix`,
	} {
		body := pageContextRequest(t, pageContextItem("browser_tabs", output))
		got, changed := normalizeCodexUntrustedContext(body)
		require.False(t, changed, output)
		require.Equal(t, body, got)
	}
}

func TestNormalizeCodexUntrustedContextMixedBootstrap(t *testing.T) {
	for _, bootstrap := range []map[string]any{
		{"type": "function_call_output", "namespace": "codex_app", "name": "create_thread", "output": delegationEnvelope},
		{"type": "function_call_output", "namespace": "codex_app", "name": "automation_update", "output": "<heartbeat><automation_id>test</automation_id></heartbeat>"},
	} {
		body := pageContextRequest(t, pageContextItem("browser_tabs", codexBrowserTabsOutput), bootstrap)
		got, changed := normalizeCodexUntrustedContext(body)
		require.True(t, changed)
		got, _ = normalizeCodexAutomationBootstrap(got)
		got, _ = normalizeCodexDelegationBootstrap(got)
		require.False(t, service.ValidateFunctionCallOutputContextBytes(got).HasFunctionCallOutput)
		require.Len(t, gjson.GetBytes(got, "input").Array(), 2)
	}
}

func TestOpenAIResponses_NormalizesCodexContextBeforeToolValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		item     map[string]any
		accepted bool
	}{
		{name: "page", item: pageContextItem("codex_apps_open_page", `{"page_id":null}`), accepted: true},
		{name: "goal", item: pageContextItem("goal_objective", "complete the task"), accepted: true},
		{name: "terminal", item: pageContextItem("terminal_info", "working directory"), accepted: true},
		{name: "browser", item: pageContextItem("browser_tabs", codexBrowserTabsOutput), accepted: true},
		{name: "ordinary tool", item: pageContextItem("read_file", "result")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reachedConcurrency := false
			h := newOpenAIHandlerForPreviousResponseIDValidation(t, &concurrencyCacheMock{
				acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
					reachedConcurrency = true
					// Stop after preflight, without selecting or contacting an upstream.
					return false, &WaitQueueFullError{SlotType: "user"}
				},
			})
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(pageContextRequest(t, tc.item)))
			c.Request.Header.Set("Content-Type", "application/json")
			groupID := int64(2)
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
				ID: 101, GroupID: &groupID, User: &service.User{ID: 1},
			})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
			h.Responses(c)
			require.Equal(t, tc.accepted, reachedConcurrency, w.Body.String())
			if tc.accepted {
				require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
				require.NotContains(t, w.Body.String(), "function_call_output requires call_id")
			} else {
				require.Equal(t, http.StatusBadRequest, w.Code)
				require.Contains(t, w.Body.String(), "function_call_output requires call_id")
			}
		})
	}
}
