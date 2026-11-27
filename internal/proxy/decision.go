package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
)

// requestUserInputTool 是 Codex 内置的结构化提问工具名。
const requestUserInputTool = "request_user_input"

// decisionOption / decisionQuestion 对应 Codex request_user_input 的参数结构。
type decisionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type decisionQuestion struct {
	ID       string           `json:"id"`
	Header   string           `json:"header"`
	Question string           `json:"question"`
	Options  []decisionOption `json:"options"`
}

type decisionArguments struct {
	Questions []decisionQuestion `json:"questions"`
}

var (
	// 上游把结构化选择渲染成 <decision question="..."><option ...>...</option></decision>。
	decisionOpenRe = regexp.MustCompile(`(?is)<decision\b([^>]*)>`)
	decisionEnd    = "</decision>"
	optionTagRe    = regexp.MustCompile(`(?is)<option\b([^>]*)>(.*?)</option>`)
	htmlAttrRe     = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	fenceOpenRe    = regexp.MustCompile("(?is)```[a-z0-9_-]*\\s*$")
)

// decisionToolAllowed 判断本次请求是否允许返回 request_user_input 调用。
func decisionToolAllowed(allowed map[string]bool, mode, name string) bool {
	if mode == "none" || !allowed[requestUserInputTool] {
		return false
	}
	if mode == "function" && name != "" && name != requestUserInputTool {
		return false
	}
	return true
}

// extractDecisionCalls 把回复中的 <decision>/<option> 片段转换为
// request_user_input 调用，并返回移除片段后的正文。
func extractDecisionCalls(text string, allowed bool) (string, []ToolCall) {
	if !allowed || !strings.Contains(text, "<decision") {
		return text, nil
	}

	var questions []decisionQuestion
	clean := text
	for len(questions) < 3 {
		open := decisionOpenRe.FindStringSubmatchIndex(clean)
		if open == nil {
			break
		}
		rest := clean[open[1]:]
		end := strings.Index(strings.ToLower(rest), decisionEnd)
		if end < 0 {
			break
		}
		attrs := clean[open[2]:open[3]]
		body := rest[:end]
		blockEnd := open[1] + end + len(decisionEnd)

		question := strings.TrimSpace(html.UnescapeString(attrValue(attrs, "question")))
		options := parseDecisionOptions(body)
		if question == "" || len(options) == 0 {
			break
		}
		if len(questions) == 0 {
			questions = append(questions, decisionQuestion{
				ID:       "next_step",
				Header:   "下一步",
				Question: question,
				Options:  options,
			})
		} else {
			questions = append(questions, decisionQuestion{
				ID:       fmt.Sprintf("next_step_%d", len(questions)+1),
				Header:   "下一步",
				Question: question,
				Options:  options,
			})
		}

		start, stop := trimDecisionFence(clean, open[0], blockEnd)
		before, after := clean[:start], clean[stop:]
		if hasVisibleText(before) && hasVisibleText(after) {
			clean = strings.TrimRight(before, " \t\r\n") + "\n\n" + strings.TrimLeft(after, " \t\r\n")
		} else {
			clean = before + after
		}
	}

	if len(questions) == 0 {
		return text, nil
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(decisionArguments{Questions: questions}); err != nil {
		return text, nil
	}
	args := strings.TrimRight(buf.String(), "\n")
	call := ToolCall{
		ID: "call_" + strings.ReplaceAll(uuid(), "-", ""),
		Function: FunctionCall{
			Name:      requestUserInputTool,
			Arguments: args,
		},
	}
	return strings.TrimRight(clean, " \t\r\n"), []ToolCall{call}
}

// parseDecisionOptions 解析决策块内的 <option description="...">label</option>。
func parseDecisionOptions(body string) []decisionOption {
	matches := optionTagRe.FindAllStringSubmatch(body, -1)
	options := make([]decisionOption, 0, len(matches))
	for _, m := range matches {
		label := strings.TrimSpace(html.UnescapeString(stripHTMLTags(m[2])))
		if label == "" {
			continue
		}
		description := strings.TrimSpace(html.UnescapeString(attrValue(m[1], "description")))
		if description == "" {
			description = label
		}
		options = append(options, decisionOption{Label: label, Description: description})
	}
	return options
}

func attrValue(attrs, name string) string {
	for _, m := range htmlAttrRe.FindAllStringSubmatch(attrs, -1) {
		if !strings.EqualFold(m[1], name) {
			continue
		}
		if m[2] != "" {
			return m[2]
		}
		return m[3]
	}
	return ""
}

// stripHTMLTags 去掉 label 里可能出现的简单标签，保留纯文本。
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// trimDecisionFence 把包裹决策块的 Markdown 代码围栏一并纳入删除范围。
func trimDecisionFence(text string, start, end int) (int, int) {
	if m := fenceOpenRe.FindStringIndex(text[:start]); m != nil {
		if strings.TrimSpace(text[m[1]:start]) == "" {
			start = m[0]
		}
	}
	tail := text[end:]
	trimmed := strings.TrimLeft(tail, " \t\r\n")
	if strings.HasPrefix(trimmed, "```") {
		lead := len(tail) - len(trimmed)
		if i := strings.IndexByte(trimmed[3:], '\n'); i < 0 {
			end = len(text)
		} else {
			end += lead + 3
		}
	}
	return start, end
}

// isExecClient 判断请求是否来自非交互的 codex exec。该模式不支持
// request_user_input，转换后会直接报错，因此保持原样返回文本。
func isExecClient(meta map[string]json.RawMessage) bool {
	if len(meta) == 0 {
		return false
	}
	var encoded string
	if err := json.Unmarshal(meta["x-codex-turn-metadata"], &encoded); err != nil {
		return false
	}
	var turn struct {
		Trigger string `json:"turn_trigger"`
	}
	if err := json.Unmarshal([]byte(encoded), &turn); err != nil {
		return false
	}
	return turn.Trigger == "exec"
}

// requestUserInputRejected 检测本轮输入里是否已经带着客户端对该工具的拒绝结果，
// 例如 codex 默认模式未开启 default_mode_request_user_input 时返回的错误。
// 命中后本轮不再转换，避免模型反复生成同一张选项卡。
func requestUserInputRejected(input any) bool {
	items, ok := input.([]any)
	if !ok {
		return false
	}
	calls := make(map[string]bool)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := item["type"].(string)
		if strings.EqualFold(strings.TrimSpace(itemType), "function_call") {
			if name, _ := item["name"].(string); name == requestUserInputTool {
				callID, _ := item["call_id"].(string)
				if callID != "" {
					calls[callID] = true
				}
			}
		}
	}
	if len(calls) == 0 {
		return false
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := item["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(itemType), "function_call_output") {
			continue
		}
		callID, _ := item["call_id"].(string)
		if !calls[callID] {
			continue
		}
		if strings.Contains(extractResponsesContent(item["output"]), "request_user_input is unavailable") {
			return true
		}
	}
	return false
}
