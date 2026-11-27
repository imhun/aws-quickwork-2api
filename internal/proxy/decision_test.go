package proxy

import (
	"encoding/json"
	"testing"
)

func TestExtractDecisionCallsBasic(t *testing.T) {
	text := `<decision question="下一步要我准备什么？">
  <option description="把 captureScreen 里依赖 olderRows 的分支逐个列出来">起草 R0-3 的删改清单</option>
  <option description="设计 debug 构建里录 JSONL 轨迹的埋点位置和回放器接口">起草 R1 轨迹录制和回放</option>
  <option description="把 pager-enter.patch 改写成 gateway Live 处理里的门禁">起草 P0-1 的 gateway Enter 门禁</option>
</decision>`

	gotText, calls := extractDecisionCalls(text, true)

	if gotText != "" {
		t.Fatalf("remaining text = %q, want empty", gotText)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Function.Name != requestUserInputTool {
		t.Fatalf("tool name = %q, want %q", calls[0].Function.Name, requestUserInputTool)
	}
	want := `{"questions":[{"id":"next_step","header":"下一步","question":"下一步要我准备什么？","options":[{"label":"起草 R0-3 的删改清单","description":"把 captureScreen 里依赖 olderRows 的分支逐个列出来"},{"label":"起草 R1 轨迹录制和回放","description":"设计 debug 构建里录 JSONL 轨迹的埋点位置和回放器接口"},{"label":"起草 P0-1 的 gateway Enter 门禁","description":"把 pager-enter.patch 改写成 gateway Live 处理里的门禁"}]}]}`
	if calls[0].Function.Arguments != want {
		t.Fatalf("arguments = %s\nwant %s", calls[0].Function.Arguments, want)
	}
}

func TestExtractDecisionCallsKeepsSurroundingText(t *testing.T) {
	text := "先给结论。\n\n<decision question=\"继续吗？\"><option description=\"继续\">继续</option><option description=\"停止\">停止</option></decision>\n\n细节在下面。"

	gotText, calls := extractDecisionCalls(text, true)

	if gotText != "先给结论。\n\n细节在下面。" {
		t.Fatalf("remaining text = %q", gotText)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
}

func TestExtractDecisionCallsDisabled(t *testing.T) {
	text := `<decision question="继续吗？"><option description="继续">继续</option></decision>`

	gotText, calls := extractDecisionCalls(text, false)

	if gotText != text {
		t.Fatalf("remaining text = %q, want original", gotText)
	}
	if calls != nil {
		t.Fatalf("calls = %v, want nil", calls)
	}
}

func TestExtractDecisionCallsMissingDescription(t *testing.T) {
	text := `<decision question="选哪个？"><option>方案 A</option><option>方案 B</option></decision>`

	_, calls := extractDecisionCalls(text, true)

	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	want := `{"questions":[{"id":"next_step","header":"下一步","question":"选哪个？","options":[{"label":"方案 A","description":"方案 A"},{"label":"方案 B","description":"方案 B"}]}]}`
	if calls[0].Function.Arguments != want {
		t.Fatalf("arguments = %s\nwant %s", calls[0].Function.Arguments, want)
	}
}

func TestExtractDecisionCallsUnescapesEntities(t *testing.T) {
	text := `<decision question="A &amp; B 怎么选？"><option description="用 &quot;甲&quot;">甲 &amp; 乙</option><option description='单引号'>丙</option></decision>`

	_, calls := extractDecisionCalls(text, true)

	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	want := `{"questions":[{"id":"next_step","header":"下一步","question":"A & B 怎么选？","options":[{"label":"甲 & 乙","description":"用 \"甲\""},{"label":"丙","description":"单引号"}]}]}`
	if calls[0].Function.Arguments != want {
		t.Fatalf("arguments = %s\nwant %s", calls[0].Function.Arguments, want)
	}
}

func TestExtractDecisionCallsStripsFence(t *testing.T) {
	text := "```html\n<decision question=\"继续吗？\"><option description=\"继续\">继续</option></decision>\n```"

	gotText, calls := extractDecisionCalls(text, true)

	if gotText != "" {
		t.Fatalf("remaining text = %q, want empty", gotText)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
}

func TestExtractDecisionCallsInvalidKeepsText(t *testing.T) {
	text := `<decision question="继续吗？"></decision>`

	gotText, calls := extractDecisionCalls(text, true)

	if gotText != text {
		t.Fatalf("remaining text = %q, want original", gotText)
	}
	if calls != nil {
		t.Fatalf("calls = %v, want nil", calls)
	}
}

func TestDecisionToolAllowed(t *testing.T) {
	allowed := map[string]bool{requestUserInputTool: true, "exec_command": true}
	cases := []struct {
		mode, name string
		allowed    map[string]bool
		want       bool
	}{
		{"auto", "", allowed, true},
		{"none", "", allowed, false},
		{"function", requestUserInputTool, allowed, true},
		{"function", "exec_command", allowed, false},
		{"auto", "", map[string]bool{"exec_command": true}, false},
	}
	for _, c := range cases {
		if got := decisionToolAllowed(c.allowed, c.mode, c.name); got != c.want {
			t.Fatalf("decisionToolAllowed(mode=%q,name=%q) = %t, want %t", c.mode, c.name, got, c.want)
		}
	}
}

func TestIsExecClient(t *testing.T) {
	execMeta := map[string]json.RawMessage{
		"x-codex-turn-metadata": json.RawMessage(`"{\"turn_trigger\":\"exec\"}"`),
	}
	tuiMeta := map[string]json.RawMessage{
		"x-codex-turn-metadata": json.RawMessage(`"{\"turn_trigger\":\"user\"}"`),
	}
	if !isExecClient(execMeta) {
		t.Fatal("exec turn should be detected")
	}
	if isExecClient(tuiMeta) {
		t.Fatal("interactive turn should not be treated as exec")
	}
	if isExecClient(nil) {
		t.Fatal("missing metadata should not be treated as exec")
	}
}

func TestRequestUserInputRejected(t *testing.T) {
	call := map[string]any{"type": "function_call", "call_id": "call_1", "name": requestUserInputTool, "arguments": "{}"}
	out := func(callID, text string) map[string]any {
		return map[string]any{"type": "function_call_output", "call_id": callID, "output": text}
	}

	if !requestUserInputRejected([]any{call, out("call_1", "request_user_input is unavailable in default mode")}) {
		t.Fatal("unavailable output should disable conversion")
	}
	if requestUserInputRejected([]any{call, out("call_1", "{\"answers\":{\"next_step\":{\"answers\":[\"A\"]}}}")}) {
		t.Fatal("successful answer should keep conversion enabled")
	}
	if requestUserInputRejected([]any{call, out("call_2", "request_user_input is unavailable in default mode")}) {
		t.Fatal("output for another call should not disable conversion")
	}
	if requestUserInputRejected("plain input") {
		t.Fatal("string input should not disable conversion")
	}
}
