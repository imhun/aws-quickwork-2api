package proxy

import (
	"encoding/json"
	"testing"
)

func TestExpandNamespaceToolsKeepsCustomTools(t *testing.T) {
	tools := []responsesTool{{
		Type:        "custom",
		Name:        "apply_patch",
		Description: "FREEFORM patching",
		Format:      map[string]any{"type": "grammar"},
	}}
	got, origins := expandNamespaceTools(tools)
	if len(got) != 1 {
		t.Fatalf("got %d tools, want 1", len(got))
	}
	tool := got[0]
	if !tool.Custom || tool.Type != "custom" {
		t.Fatalf("tool = %#v, want custom", tool)
	}
	if tool.Function.Name != "apply_patch" {
		t.Fatalf("name = %q, want apply_patch", tool.Function.Name)
	}
	if origins["apply_patch"].name != "" {
		t.Fatalf("unexpected origin: %#v", origins["apply_patch"])
	}
	schema, ok := tool.Function.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want object schema", tool.Function.Parameters)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "input" {
		t.Fatalf("required = %#v, want [input]", schema["required"])
	}
}

func TestCustomToolInputAcceptsWrappedForms(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"object", `{"input":"*** Begin Patch\n*** End Patch"}`, "*** Begin Patch\n*** End Patch"},
		{"string", `"raw text"`, "raw text"},
		{"patch key", `{"patch":"p"}`, "p"},
		{"plain", `not json`, "not json"},
		{"empty", ``, ""},
	}
	for _, tc := range cases {
		if got := customToolInput(tc.args); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBuildResponsesOutputItemsUsesCustomToolCall(t *testing.T) {
	calls := []ToolCall{{
		ID:     "call_1",
		Type:   "custom",
		Custom: true,
		Function: FunctionCall{
			Name:      "apply_patch",
			Arguments: `{"input":"*** Begin Patch\n*** End Patch"}`,
		},
	}}
	items := buildResponsesOutputItemsWithThinking("msg_1", "", "editing", calls)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	item := items[1]
	if item["type"] != "custom_tool_call" {
		t.Fatalf("type = %v, want custom_tool_call", item["type"])
	}
	if item["name"] != "apply_patch" || item["call_id"] != "call_1" {
		t.Fatalf("unexpected item: %#v", item)
	}
	if item["input"] != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("input = %q", item["input"])
	}
	if _, ok := item["arguments"]; ok {
		t.Fatal("custom_tool_call must not carry arguments")
	}
}

func TestParseResponsesMessagesCustomToolRoundTrip(t *testing.T) {
	input := []any{
		map[string]any{
			"type":    "custom_tool_call",
			"call_id": "call_9",
			"name":    "apply_patch",
			"input":   "*** Begin Patch\n*** End Patch",
		},
		map[string]any{
			"type":    "custom_tool_call_output",
			"call_id": "call_9",
			"output":  "Success",
		},
	}
	msgs, err := parseResponsesMessages("", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	call := msgs[0].ToolCalls[0]
	if !call.Custom || call.Type != "custom" || call.Function.Name != "apply_patch" {
		t.Fatalf("unexpected call: %#v", call)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not JSON: %v", err)
	}
	if args["input"] != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("input = %q", args["input"])
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "call_9" || msgs[1].Content != "Success" {
		t.Fatalf("unexpected tool message: %#v", msgs[1])
	}
}

func TestMarkCustomCallsMatchesFlattenedNames(t *testing.T) {
	defs := []toolDefinition{{Name: "apply_patch", Custom: true}, {Name: "exec_command"}}
	calls := []ToolCall{{Function: FunctionCall{Name: "apply_patch"}}, {Function: FunctionCall{Name: "exec_command"}}}
	markCustomCalls(calls, customToolNames(defs))
	if !calls[0].Custom {
		t.Fatal("apply_patch should be custom")
	}
	if calls[1].Custom {
		t.Fatal("exec_command should not be custom")
	}
}
