package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quickwork-openai/internal/config"
)

func TestParseResponsesMessagesStringInput(t *testing.T) {
	msgs, err := parseResponsesMessages("Be concise.", "Hello QuickWork")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "Be concise." {
		t.Fatalf("unexpected system message: %#v", msgs[0])
	}
	if msgs[1].Role != "user" || msgs[1].Content != "Hello QuickWork" {
		t.Fatalf("unexpected user message: %#v", msgs[1])
	}
}

func TestParseResponsesMessagesArrayWithToolLoop(t *testing.T) {
	input := []any{
		map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "Check weather in Taipei."},
			},
		},
		map[string]any{
			"type":      "function_call",
			"call_id":   "call_123",
			"name":      "get_weather",
			"arguments": `{"city":"Taipei"}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_123",
			"output":  `{"temp":"24C"}`,
		},
	}

	msgs, err := parseResponsesMessages("", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("unexpected assistant tool call message: %#v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_123" || msgs[2].Content != `{"temp":"24C"}` {
		t.Fatalf("unexpected tool output message: %#v", msgs[2])
	}

	prompt, err := formatPrompt(msgs)
	if err != nil {
		t.Fatalf("formatPrompt error: %v", err)
	}
	if !strings.Contains(prompt, "get_weather") || !strings.Contains(prompt, "call_id=call_123") {
		t.Fatalf("formatted prompt missing tool history:\n%s", prompt)
	}
}

func TestToChatToolsSupportsFlatAndNested(t *testing.T) {
	tools := toChatTools([]responsesTool{
		{
			Type:        "function",
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object"},
		},
		{
			Type: "function",
			Function: &ToolFunction{
				Name: "write_file",
			},
		},
		{
			Type: "web_search",
		},
	})
	if len(tools) != 2 {
		t.Fatalf("expected 2 function tools, got %d", len(tools))
	}
	if tools[0].Function.Name != "read_file" || tools[1].Function.Name != "write_file" {
		t.Fatalf("unexpected converted tools: %#v", tools)
	}
}

func TestResponsesRoutesAndPathNormalization(t *testing.T) {
	cfg := config.Default()
	srv := NewServer(&cfg, nil)
	h := srv.Handler()

	// 確認 /v1/responses、/v1/v1/responses、/responses 不會回傳 404（GET 應回 405 Method Not Allowed）。
	for _, path := range []string{"/v1/responses", "/v1/v1/responses", "/responses", "/v1/responses/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("path %s: expected 405, got %d", path, rec.Code)
		}
	}

	// 確認無 body 的 POST /v1/responses 會進入 handler 回傳 400，而非 404。
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /v1/responses empty body: expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestExpandNamespaceToolsFlattensChildren(t *testing.T) {
	tools := []responsesTool{
		{Type: "function", Name: "exec_command", Description: "run a command"},
		{Type: "web_search"},
		{Type: "namespace", Name: "mcp__patchloom", Description: "server wide", Tools: []responsesTool{
			{Type: "function", Name: "append_file", Description: "append", Parameters: map[string]any{"type": "object"}},
			{Type: "function", Name: "server_info"},
		}},
	}

	chat, origins := expandNamespaceTools(tools)

	if len(chat) != 3 {
		t.Fatalf("expected 3 bridged tools, got %d: %#v", len(chat), chat)
	}
	if got := chat[0].Function.Name; got != "exec_command" {
		t.Fatalf("plain tool name = %q, want %q", got, "exec_command")
	}
	if got := chat[1].Function.Name; got != "mcp__patchloom_append_file" {
		t.Fatalf("namespaced tool name = %q, want %q", got, "mcp__patchloom_append_file")
	}
	if got := chat[1].Function.Description; got != "append" {
		t.Fatalf("namespaced tool description = %q, want %q", got, "append")
	}
	if chat[1].Function.Parameters == nil {
		t.Fatal("namespaced tool lost its parameters")
	}
	if got := chat[2].Function.Name; got != "mcp__patchloom_server_info" {
		t.Fatalf("second namespaced tool name = %q, want %q", got, "mcp__patchloom_server_info")
	}
	if got := origins["mcp__patchloom_server_info"]; got != (toolOrigin{namespace: "mcp__patchloom", name: "server_info"}) {
		t.Fatalf("origin = %#v, want namespace mcp__patchloom name server_info", got)
	}
	if _, ok := origins["exec_command"]; ok {
		t.Fatal("plain tool should not be recorded as a namespaced origin")
	}
}

func TestApplyToolOriginsRestoresNamespace(t *testing.T) {
	origins := map[string]toolOrigin{
		"mcp__patchloom_append_file": {namespace: "mcp__patchloom", name: "append_file"},
	}
	calls := applyToolOrigins([]ToolCall{
		{ID: "call_1", Function: FunctionCall{Name: "mcp__patchloom_append_file", Arguments: `{"path":"a"}`}},
		{ID: "call_2", Function: FunctionCall{Name: "exec_command", Arguments: `{"cmd":"ls"}`}},
	}, origins)

	if got := calls[0].Namespace; got != "mcp__patchloom" {
		t.Fatalf("call_1 namespace = %q, want %q", got, "mcp__patchloom")
	}
	if got := calls[0].Function.Name; got != "append_file" {
		t.Fatalf("call_1 name = %q, want %q", got, "append_file")
	}
	if got := calls[0].Function.Arguments; got != `{"path":"a"}` {
		t.Fatalf("call_1 arguments = %q, want %q", got, `{"path":"a"}`)
	}
	if got := calls[1].Namespace; got != "" {
		t.Fatalf("call_2 namespace = %q, want empty", got)
	}
	if got := calls[1].Function.Name; got != "exec_command" {
		t.Fatalf("call_2 name = %q, want %q", got, "exec_command")
	}
}

func TestBuildResponsesOutputItemsCarriesNamespace(t *testing.T) {
	items := buildResponsesOutputItems("msg_1", "", []ToolCall{
		{
			ID:        "call_1",
			Namespace: "mcp__patchloom",
			Function:  FunctionCall{Name: "append_file", Arguments: `{"path":"a"}`},
		},
	})

	if len(items) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(items))
	}
	if got := items[0]["namespace"]; got != "mcp__patchloom" {
		t.Fatalf("namespace = %v, want %q", got, "mcp__patchloom")
	}
	if got := items[0]["name"]; got != "append_file" {
		t.Fatalf("name = %v, want %q", got, "append_file")
	}
}

func TestParseResponsesMessagesQualifiesNamespacedCall(t *testing.T) {
	input := []any{
		map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": "call the tool"}},
		},
		map[string]any{
			"type":      "function_call",
			"call_id":   "call_1",
			"namespace": "mcp__patchloom",
			"name":      "append_file",
			"arguments": `{"path":"a"}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_1",
			"output":  "ok",
		},
	}

	msgs, err := parseResponsesMessages("", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if got := msgs[1].ToolCalls[0].Function.Name; got != "mcp__patchloom_append_file" {
		t.Fatalf("history tool name = %q, want %q", got, "mcp__patchloom_append_file")
	}
	if got := qualifyNamespacedName("mcp__patchloom_append_file", "mcp__patchloom"); got != "mcp__patchloom_append_file" {
		t.Fatalf("already-qualified name = %q, want unchanged", got)
	}
	if got := qualifyNamespacedName("append_file", nil); got != "append_file" {
		t.Fatalf("name without namespace = %q, want unchanged", got)
	}
}

func TestUpstreamThinkingEffortMapping(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"minimal":    "low",
		"low":        "low",
		"medium":     "medium",
		"high":       "high",
		"xhigh":      "high",
		"ultra":      "high",
		"persistent": "high",
		"none":       "",
		"bogus":      "",
	}
	for in, want := range cases {
		if got := upstreamThinkingEffort(in); got != want {
			t.Fatalf("upstreamThinkingEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildResponsesOutputItemsWithThinking(t *testing.T) {
	output := buildResponsesOutputItemsWithThinking("msg_1", "let me think", "answer", nil)
	if len(output) != 2 {
		t.Fatalf("expected 2 output items, got %d", len(output))
	}
	if got := output[0]["type"]; got != "reasoning" {
		t.Fatalf("first item type = %v, want reasoning", got)
	}
	summary, ok := output[0]["summary"].([]map[string]any)
	if !ok || len(summary) != 1 {
		t.Fatalf("reasoning summary = %#v, want one summary_text part", output[0]["summary"])
	}
	if got := summary[0]["text"]; got != "let me think" {
		t.Fatalf("reasoning text = %v, want %q", got, "let me think")
	}
	if got := output[1]["type"]; got != "message" {
		t.Fatalf("second item type = %v, want message", got)
	}

	without := buildResponsesOutputItemsWithThinking("msg_2", "", "answer", nil)
	if len(without) != 1 || without[0]["type"] != "message" {
		t.Fatalf("empty thinking should yield only the message item, got %#v", without)
	}
}
