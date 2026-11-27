package proxy

import "testing"

func TestCompactSchemaDropsNoiseAndTruncates(t *testing.T) {
	in := map[string]any{
		"type":        "object",
		"description": "这是一个很长的工具描述，用来验证压缩后会保留开头并截断，不保留示例和默认值。",
		"examples":    []any{map[string]any{"a": 1}},
		"default":     "x",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "文件路径，一段同样很长的说明文字，用于验证递归压缩行为是否生效。",
				"format":      "uri",
			},
		},
		"required": []any{"path"},
	}
	got := compactSchema(in).(map[string]any)
	if _, ok := got["examples"]; ok {
		t.Fatal("examples should be dropped")
	}
	if _, ok := got["default"]; ok {
		t.Fatal("default should be dropped")
	}
	wantDesc := truncateRunes(in["description"].(string), 40)
	if got["description"] != wantDesc {
		t.Fatalf("description = %q want %q", got["description"], wantDesc)
	}
	props := got["properties"].(map[string]any)
	path := props["path"].(map[string]any)
	if _, ok := path["format"]; ok {
		t.Fatal("format should be dropped")
	}
	if path["type"] != "string" {
		t.Fatalf("type lost: %v", path["type"])
	}
	if got["type"] != "object" {
		t.Fatalf("root type lost: %v", got["type"])
	}
}

func TestSelectNeutralToolsPrefersCoreTools(t *testing.T) {
	defs := []toolDefinition{
		{Name: "list_mcp_resources"},
		{Name: "mcp__patchloom_read_file"},
		{Name: "exec_command"},
		{Name: "mcp__zvec_grep_zvec_grep_search"},
		{Name: "write_stdin"},
	}
	got := selectNeutralTools(defs, 3)
	want := []string{"exec_command", "write_stdin", "mcp__patchloom_read_file"}
	if len(got) != len(want) {
		t.Fatalf("got %d tools want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("tool %d = %q want %q", i, got[i].Name, name)
		}
	}
}

func TestSelectNeutralToolsKeepsAllWhenUnderLimit(t *testing.T) {
	defs := []toolDefinition{{Name: "exec_command"}, {Name: "write_stdin"}}
	got := selectNeutralTools(defs, 6)
	if len(got) != 2 || got[0].Name != "exec_command" || got[1].Name != "write_stdin" {
		t.Fatalf("unexpected result: %#v", got)
	}
}
