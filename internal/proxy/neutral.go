package proxy

// neutralSystemPrompt 是中性精简模式发给上游的系统提示。它刻意不提调用方
// 是哪个 agent，只描述任务和工具，避免触发上游的身份护栏。
const neutralSystemPrompt = "我在本机准备了下面这些工具。" +
	"你通过它们读写文件、执行命令和修改代码；这是本会话唯一可用的操作通道。" +
	"消息开头如果给出项目约定和环境信息，按它们执行。" +
	"直接完成任务并给出结果，不要介绍自己。"

// neutralToolPriority 是精简模式下优先保留的工具：先保证能跑命令、改文件，
// 其余工具只在还有余量时补进来。
var neutralToolPriority = []string{
	"exec_command", "write_stdin",
	"apply_patch", "update_plan",
	"mcp__patchloom_read_file", "mcp__patchloom_search_files",
	"mcp__patchloom_replace_text", "mcp__patchloom_execute_plan",
	"mcp__zvec_grep_zvec_grep_search",
	"mcp__fff_grep", "mcp__fff_find_files",
	"view_image",
}

// selectNeutralTools 把工具面收敛到 limit 个，避免几十个 schema 一次塞给上游。
func selectNeutralTools(defs []toolDefinition, limit int) []toolDefinition {
	if limit <= 0 || len(defs) <= limit {
		return defs
	}
	keep := make([]toolDefinition, 0, limit)
	used := make(map[string]bool, limit)
	for _, name := range neutralToolPriority {
		for _, d := range defs {
			if d.Name == name && !used[d.Name] {
				keep = append(keep, d)
				used[d.Name] = true
			}
		}
		if len(keep) >= limit {
			return keep
		}
	}
	for _, d := range defs {
		if len(keep) >= limit {
			break
		}
		if !used[d.Name] {
			keep = append(keep, d)
			used[d.Name] = true
		}
	}
	return keep
}

// compactToolDefinitions 压缩工具定义：截短描述、去掉示例和冗余字段，
// 只保留名称、参数结构和必填项。上游对超大 schema 有内容检查。
func compactToolDefinitions(defs []toolDefinition) []toolDefinition {
	out := make([]toolDefinition, 0, len(defs))
	for _, d := range defs {
		out = append(out, toolDefinition{
			Name:        d.Name,
			Description: truncateRunes(d.Description, 60),
			Parameters:  compactSchema(d.Parameters),
			Custom:      d.Custom,
		})
	}
	return out
}

// compactSchema 递归清理 JSON schema：保留结构信息，丢弃说明性长文本。
func compactSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			switch k {
			case "description", "title":
				if s, ok := val.(string); ok && s != "" {
					out["description"] = truncateRunes(s, 40)
				}
			case "examples", "default", "$schema", "format", "pattern":
				continue
			case "properties":
				if props, ok := val.(map[string]any); ok {
					compact := make(map[string]any, len(props))
					for pk, pv := range props {
						compact[pk] = compactSchema(pv)
					}
					out[k] = compact
					continue
				}
				out[k] = compactSchema(val)
			default:
				out[k] = compactSchema(val)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			out = append(out, compactSchema(item))
		}
		return out
	default:
		return v
	}
}

// truncateRunes 按字符数截断，避免切断多字节字符。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
