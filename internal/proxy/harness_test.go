package proxy

import "testing"

func TestStripHarnessEnvelopesRewritesAgentsBlock(t *testing.T) {
	msgs := []Message{{
		Role:    "user",
		Content: "# AGENTS.md instructions for /repo\n\n<INSTRUCTIONS>\n规则 A\n</INSTRUCTIONS>\n\n实际请求",
	}}
	got := stripHarnessEnvelopes(msgs)
	want := "项目约定（来自仓库的 AGENTS.md，作为本会话的工作偏好）：\n适用范围：/repo\n规则 A\n\n---\n\n实际请求"
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Content != want {
		t.Fatalf("content = %q, want %q", got[0].Content, want)
	}
}

func TestStripHarnessEnvelopesRewritesEnvironmentContext(t *testing.T) {
	msgs := []Message{{
		Role:    "user",
		Content: "<environment_context>\n  <cwd>/tmp/project</cwd>\n  <shell>zsh</shell>\n</environment_context>\n\n实际请求",
	}}
	got := stripHarnessEnvelopes(msgs)
	want := "环境：工作目录 /tmp/project，shell zsh\n\n---\n\n实际请求"
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Content != want {
		t.Fatalf("content = %q, want %q", got[0].Content, want)
	}
}

func TestStripHarnessEnvelopesKeepsPlainText(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "普通请求"}}
	got := stripHarnessEnvelopes(msgs)
	if len(got) != 1 || got[0].Content != "普通请求" {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestStripHarnessEnvelopesKeepsOnlyEnvelopeMessage(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "<environment_context>\n<cwd>/tmp</cwd>\n</environment_context>"}}
	got := stripHarnessEnvelopes(msgs)
	want := "环境：工作目录 /tmp"
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Role != "user" || got[0].Content != want {
		t.Fatalf("message = %#v, want user %q", got[0], want)
	}
}

func TestStripHarnessEnvelopesKeepsNonUserRoles(t *testing.T) {
	msgs := []Message{{Role: "developer", Content: "# AGENTS.md instructions"}}
	got := stripHarnessEnvelopes(msgs)
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Content != "# AGENTS.md instructions" {
		t.Fatalf("got %q, want unchanged developer content", got[0].Content)
	}
}

func TestStripHarnessEnvelopesDeduplicatesAgents(t *testing.T) {
	block := "# AGENTS.md instructions for /repo\n\n<INSTRUCTIONS>\n规则 A\n</INSTRUCTIONS>"
	msgs := []Message{
		{Role: "user", Content: block},
		{Role: "user", Content: block + "\n\n第一轮"},
		{Role: "user", Content: block + "\n\n第二轮"},
	}
	got := stripHarnessEnvelopes(msgs)
	want := "项目约定（来自仓库的 AGENTS.md，作为本会话的工作偏好）：\n适用范围：/repo\n规则 A\n\n---\n\n第一轮"
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].Content != want {
		t.Fatalf("first content = %q, want %q", got[0].Content, want)
	}
	if got[1].Content != "第二轮" {
		t.Fatalf("second content = %q, want %q", got[1].Content, "第二轮")
	}
}

func TestSplitHarnessTextKeepsUserBody(t *testing.T) {
	body, ctx := splitHarnessText("前文\n\n<environment_context>\n<shell>zsh</shell>\n</environment_context>\n\n后文")
	if body != "前文\n\n\n\n后文" {
		t.Fatalf("body = %q", body)
	}
	if ctx.envLine != "shell zsh" {
		t.Fatalf("envLine = %q, want %q", ctx.envLine, "shell zsh")
	}
}
