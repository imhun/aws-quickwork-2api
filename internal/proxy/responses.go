package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"quickwork-openai/internal/accounts"
	"quickwork-openai/internal/store"
)

// ResponsesRequest 代表 OpenAI Responses API (/v1/responses) 的請求結構。
type ResponsesRequest struct {
	Model             string                     `json:"model"`
	Instructions      string                     `json:"instructions,omitempty"`
	Input             any                        `json:"input"`
	Stream            bool                       `json:"stream"`
	Tools             []responsesTool            `json:"tools,omitempty"`
	ToolChoice        any                        `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                      `json:"parallel_tool_calls,omitempty"`
	MaxOutputTokens   int                        `json:"max_output_tokens,omitempty"`
	Temperature       *float64                   `json:"temperature,omitempty"`
	Store             *bool                      `json:"store,omitempty"`
	PreviousRespID    string                     `json:"previous_response_id,omitempty"`
	PromptCacheKey    string                     `json:"prompt_cache_key,omitempty"`
	Reasoning         *responsesReasoning        `json:"reasoning,omitempty"`
	ClientMetadata    map[string]json.RawMessage `json:"client_metadata,omitempty"`
}

// responsesReasoning 是 Responses API 的思考配置（Codex 會帶上 effort/summary）。
type responsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// upstreamThinkingEffort 把客戶端的 reasoning.effort 映射為上游 thinking_effort。
func upstreamThinkingEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high", "xhigh", "max", "ultra", "persistent":
		return "high"
	default:
		return ""
	}
}

// responsesTool 同時相容 Responses API 扁平格式與 Chat Completions 巢狀格式。
type responsesTool struct {
	Type        string        `json:"type"`
	Name        string        `json:"name,omitempty"`
	Description string        `json:"description,omitempty"`
	Parameters  any           `json:"parameters,omitempty"`
	Strict      bool          `json:"strict,omitempty"`
	Function    *ToolFunction `json:"function,omitempty"`
	// Format 是 custom（freeform）工具的输入格式，例如 apply_patch 的 grammar。
	Format any `json:"format,omitempty"`
	// Tools 是 Codex 新版 namespace 聚合工具的子工具列表。
	Tools []responsesTool `json:"tools,omitempty"`
}

// toolOrigin 記録扁平化後的工具名在客戶端命名空間中的原始位置。
type toolOrigin struct {
	namespace string
	name      string
}

// toChatTools 將 Responses API 的 tools 轉換為內部統一使用的 []Tool。
func toChatTools(tools []responsesTool) []Tool {
	out, _ := expandNamespaceTools(tools)
	return out
}

// expandNamespaceTools 把客戶端的 namespace 聚合工具展開為扁平的 function 工具。
//
// Codex 對支持 tool search 的模型會把整個 MCP server 聲明成一個
// `type: "namespace"` 條目（內含 66 個子工具、描述可達幾十 KB），模型側只認
// 扁平函數名。展開後按 `<namespace>_<tool>` 命名，並記錄來源以便回傳調用時
// 還原成 namespace + name。
func expandNamespaceTools(tools []responsesTool) ([]Tool, map[string]toolOrigin) {
	out := make([]Tool, 0, len(tools))
	origins := make(map[string]toolOrigin)
	var walk func(items []responsesTool, namespace string)
	walk = func(items []responsesTool, namespace string) {
		for _, t := range items {
			typ := strings.ToLower(strings.TrimSpace(t.Type))
			switch typ {
			case "namespace":
				child := strings.TrimSpace(t.Name)
				if namespace != "" && child != "" {
					child = namespace + "_" + child
				}
				walk(t.Tools, child)
			case "", "function":
				fn := ToolFunction{
					Description: t.Description,
					Parameters:  t.Parameters,
					Strict:      t.Strict,
				}
				if t.Function != nil {
					fn = *t.Function
				}
				name := strings.TrimSpace(fn.Name)
				if name == "" {
					name = strings.TrimSpace(t.Name)
				}
				if name == "" {
					continue
				}
				fn.Name = name
				if namespace != "" {
					fn.Name = namespace + "_" + name
					origins[fn.Name] = toolOrigin{namespace: namespace, name: name}
				}
				out = append(out, Tool{Type: "function", Function: fn})
			case "custom":
				// freeform 工具（如 apply_patch）只有一段原始文本输入。桥接时把它
				// 包装成 {"input": "..."} 的 JSON 参数，回传时再还原为 custom_tool_call。
				name := strings.TrimSpace(t.Name)
				if name == "" {
					continue
				}
				if namespace != "" {
					name = namespace + "_" + name
					origins[name] = toolOrigin{namespace: namespace, name: strings.TrimSpace(t.Name)}
				}
				description := strings.TrimSpace(t.Description)
				if description != "" {
					description += "\n\n"
				}
				description += "这个工具接收自由文本输入；调用时把原始文本作为一个字符串放进 arguments.input。"
				out = append(out, Tool{
					Type:   "custom",
					Custom: true,
					Function: ToolFunction{
						Name:        name,
						Description: description,
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"input": map[string]any{
									"type":        "string",
									"description": "工具的完整自由文本输入",
								},
							},
							"required":             []any{"input"},
							"additionalProperties": false,
						},
					},
				})
			default:
				// 內建工具（如 web_search）無法由客戶端橋接執行，略過之。
			}
		}
	}
	walk(tools, "")
	return out, origins
}

// applyToolOrigins 把橋接得到的扁平工具名還原為客戶端的 namespace + 原名。
func applyToolOrigins(calls []ToolCall, origins map[string]toolOrigin) []ToolCall {
	for i := range calls {
		origin, ok := origins[strings.TrimSpace(calls[i].Function.Name)]
		if !ok {
			continue
		}
		calls[i].Namespace = origin.namespace
		calls[i].Function.Name = origin.name
	}
	return calls
}

// qualifyNamespacedName 把客戶端歷史中帶 namespace 的調用名轉回扁平形式，
// 與橋接提示裡的工具定義保持一致。缺失 namespace 時原樣返回。
func qualifyNamespacedName(name string, namespace any) string {
	name = strings.TrimSpace(name)
	ns, _ := namespace.(string)
	ns = strings.TrimSpace(ns)
	if name == "" || ns == "" || strings.HasPrefix(name, ns+"_") {
		return name
	}
	return ns + "_" + name
}

// parseResponsesMessages 將 Responses API 的 instructions 與 input 轉換為 []Message。
func parseResponsesMessages(instructions string, input any) ([]Message, error) {
	var msgs []Message
	if strings.TrimSpace(instructions) != "" {
		msgs = append(msgs, Message{
			Role:    "system",
			Content: instructions,
		})
	}

	switch v := input.(type) {
	case nil:
		return nil, fmt.Errorf("input must not be empty")
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("input must not be empty")
		}
		msgs = append(msgs, Message{Role: "user", Content: v})
		return msgs, nil
	case []any:
		for _, rawItem := range v {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			itemType, _ := item["type"].(string)
			itemType = strings.ToLower(strings.TrimSpace(itemType))

			switch itemType {
			case "", "message":
				role, _ := item["role"].(string)
				role = strings.TrimSpace(role)
				if role == "" {
					role = "user"
				}
				text := extractResponsesContent(item["content"])
				if strings.TrimSpace(text) != "" {
					msgs = append(msgs, Message{Role: role, Content: text})
				}
			case "function_call":
				callID, _ := item["call_id"].(string)
				if strings.TrimSpace(callID) == "" {
					callID, _ = item["id"].(string)
				}
				name, _ := item["name"].(string)
				name = qualifyNamespacedName(name, item["namespace"])
				args := stringifyAny(item["arguments"])
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				msgs = append(msgs, Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID:   callID,
						Type: "function",
						Function: FunctionCall{
							Name:      name,
							Arguments: args,
						},
					}},
				})
			case "custom_tool_call":
				// freeform 工具（如 apply_patch）回放时只带一段原始文本。
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				name = qualifyNamespacedName(name, item["namespace"])
				input := stringifyAny(item["input"])
				args, _ := json.Marshal(map[string]string{"input": input})
				msgs = append(msgs, Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID:     callID,
						Type:   "custom",
						Custom: true,
						Function: FunctionCall{
							Name:      name,
							Arguments: string(args),
						},
					}},
				})
			case "function_call_output", "custom_tool_call_output":
				callID, _ := item["call_id"].(string)
				output := extractResponsesContent(item["output"])
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: callID,
					Content:    output,
				})
			case "reasoning":
				// 忽略多輪上下文回傳的 reasoning 項目。
				continue
			default:
				// 其他未知項目若有 content 或 text，嘗試作為 user 內容保留。
				if text := extractResponsesContent(item["content"]); strings.TrimSpace(text) != "" {
					msgs = append(msgs, Message{Role: "user", Content: text})
				}
			}
		}
	default:
		// 若為單一物件，嘗試作為單一 message 處理。
		if item, ok := v.(map[string]any); ok {
			return parseResponsesMessages(instructions, []any{item})
		}
		return nil, fmt.Errorf("unsupported input type")
	}

	if len(msgs) == 0 {
		return nil, fmt.Errorf("input contains no messages")
	}
	return msgs, nil
}

// extractResponsesContent 解析 Responses API 的 content / output 欄位為純文字。
func extractResponsesContent(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, elem := range v {
			part, ok := elem.(map[string]any)
			if !ok {
				if s, ok := elem.(string); ok {
					b.WriteString(s)
				}
				continue
			}
			typ, _ := part["type"].(string)
			switch strings.ToLower(strings.TrimSpace(typ)) {
			case "input_text", "output_text", "text":
				if t, ok := part["text"].(string); ok {
					b.WriteString(t)
				}
			case "refusal":
				if r, ok := part["refusal"].(string); ok {
					b.WriteString(r)
				}
			case "input_image":
				b.WriteString("[image]")
			case "input_audio":
				b.WriteString("[audio]")
			case "input_file":
				b.WriteString("[file]")
			default:
				if t, ok := part["text"].(string); ok {
					b.WriteString(t)
				} else if body, err := json.Marshal(part); err == nil {
					b.Write(body)
				}
			}
		}
		return b.String()
	default:
		return stringifyAny(v)
	}
}

func stringifyAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	body, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(body)
}

func responsesUsageMap(u Usage) map[string]any {
	completion := u.CompletionTokens
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CacheReadTokens + u.CacheWriteTokens + completion
	}
	prompt := total - completion
	if prompt < 0 {
		prompt = u.PromptTokens + u.CacheReadTokens + u.CacheWriteTokens
	}
	return map[string]any{
		"input_tokens": prompt,
		"input_tokens_details": map[string]any{
			"cached_tokens": u.CacheReadTokens,
		},
		"output_tokens": completion,
		"output_tokens_details": map[string]any{
			"reasoning_tokens": 0,
		},
		"total_tokens": total,
	}
}

func buildReasoningItem(id, text string) map[string]any {
	return map[string]any{
		"id":      id,
		"type":    "reasoning",
		"summary": []map[string]any{{"type": "summary_text", "text": text}},
	}
}

func buildResponsesOutputItems(msgID, text string, toolCalls []ToolCall) []map[string]any {
	return buildResponsesOutputItemsWithThinking(msgID, "", text, toolCalls)
}

func buildResponsesOutputItemsWithThinking(msgID, thinking, text string, toolCalls []ToolCall) []map[string]any {
	output := make([]map[string]any, 0, 2+len(toolCalls))
	if strings.TrimSpace(thinking) != "" {
		output = append(output, buildReasoningItem("rs_"+strings.ReplaceAll(uuid(), "-", ""), thinking))
	}
	if strings.TrimSpace(text) != "" {
		output = append(output, map[string]any{
			"id":     msgID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		})
	}
	for _, tc := range toolCalls {
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		if tc.Custom {
			item := map[string]any{
				"id":      "ctc_" + strings.ReplaceAll(uuid(), "-", ""),
				"type":    "custom_tool_call",
				"status":  "completed",
				"call_id": tc.ID,
				"name":    tc.Function.Name,
				"input":   customToolInput(args),
			}
			if tc.Namespace != "" {
				item["namespace"] = tc.Namespace
			}
			output = append(output, item)
			continue
		}
		item := map[string]any{
			"id":        "fc_" + strings.ReplaceAll(uuid(), "-", ""),
			"type":      "function_call",
			"status":    "completed",
			"call_id":   tc.ID,
			"name":      tc.Function.Name,
			"arguments": args,
		}
		if tc.Namespace != "" {
			item["namespace"] = tc.Namespace
		}
		output = append(output, item)
	}
	return output
}

// customToolNames 收集 freeform 工具名，回传时据此选择 custom_tool_call。
func customToolNames(defs []toolDefinition) map[string]bool {
	names := make(map[string]bool)
	for _, d := range defs {
		if d.Custom {
			names[d.Name] = true
		}
	}
	return names
}

// markCustomCalls 给解析出的工具调用打上 freeform 标记。必须在还原 namespace
// 之前调用，因为 bridgeDefs 里用的是展开后的扁平名称。
func markCustomCalls(calls []ToolCall, custom map[string]bool) {
	if len(custom) == 0 {
		return
	}
	for i := range calls {
		calls[i].Custom = custom[calls[i].Function.Name]
	}
}

func buildResponseObject(respID, model, instructions, status string, created int64, output []map[string]any, usage map[string]any) map[string]any {
	if output == nil {
		output = []map[string]any{}
	}
	resp := map[string]any{
		"id":                 respID,
		"object":             "response",
		"created_at":         created,
		"status":             status,
		"error":              nil,
		"incomplete_details": nil,
		"model":              model,
		"output":             output,
	}
	if strings.TrimSpace(instructions) != "" {
		resp["instructions"] = instructions
	} else {
		resp["instructions"] = nil
	}
	if usage != nil {
		resp["usage"] = usage
	}
	return resp
}

// handleResponses 處理 OpenAI Responses API (POST /v1/responses)。
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if !s.tokens.Allowed(r) {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "invalid proxy API key")
		return
	}

	var req ResponsesRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	if err := dec.Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body: "+err.Error())
		return
	}

	msgs, err := parseResponsesMessages(req.Instructions, req.Input)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if s.cfg.NeutralPrompt {
		msgs = stripHarnessEnvelopes(msgs)
	}
	prompt, err := formatPrompt(msgs)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	chatTools, toolOrigins := expandNamespaceTools(req.Tools)
	chatReq := ChatRequest{
		Tools:      chatTools,
		ToolChoice: req.ToolChoice,
	}
	allowedTools := requestedToolNames(&chatReq)
	choiceMode, choiceName := parseToolChoice(req.ToolChoice)
	if choiceMode == "function" && choiceName != "" {
		allowedTools[choiceName] = true
	}
	parallelTools := true
	if req.ParallelToolCalls != nil {
		parallelTools = *req.ParallelToolCalls
	}
	decisionAllowed := decisionToolAllowed(allowedTools, choiceMode, choiceName) &&
		!isExecClient(req.ClientMetadata) && !requestUserInputRejected(req.Input)

	systemPromptText := systemPrompt(msgs)
	bridgeDefs := collectToolDefinitions(chatReq.Tools, chatReq.Functions)
	neutral := s.cfg.NeutralPrompt
	if neutral {
		// 中性精简模式：不转发调用方（Codex）的系统提示，只带任务说明和工具，
		// 避免上游把整段 prompt 判成"冒充另一个 agent"或超长注入。
		systemPromptText = neutralSystemPrompt
		bridgeDefs = compactToolDefinitions(selectNeutralTools(bridgeDefs, s.cfg.NeutralToolLimit))
	}
	bridgePrompt := buildToolBridgePromptFor(bridgeDefs, chatReq.ToolChoice, choiceMode, choiceName, parallelTools, neutral)
	bridgeActive := bridgePrompt != ""
	if bridgeActive {
		systemPromptText += "\n\n" + bridgePrompt
	}
	bridgeAllowed := allowedTools
	if choiceMode == "function" && choiceName != "" {
		bridgeAllowed = map[string]bool{choiceName: true}
	}
	if dump := os.Getenv("QUICKPROXY_DUMP_PROMPT"); dump != "" {
		_ = os.WriteFile(dump, []byte(systemPromptText+"\n=====PROMPT=====\n"+prompt), 0o644)
	}

	model := s.mapModel(req.Model)

	conv := s.sessions.acquire(responsesConversationKey(r, req.PromptCacheKey, msgs, systemPromptText))
	defer s.sessions.release(conv)
	if conv.turns >= maxReuseTurns {
		conv.reset()
	}
	resume := false
	promptText := prompt
	promptMessages := msgs
	if s.cfg.SessionReuse && conv.accountID != 0 && conv.systemPrompt == systemPromptText {
		if delta, ok := conv.delta(msgs); ok {
			if deltaPrompt, err := formatPrompt(delta); err == nil && hasVisibleText(deltaPrompt) {
				promptText = deltaPrompt
				promptMessages = delta
				resume = true
			}
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout.Duration())
	defer cancel()

	threadID := strings.TrimSpace(r.Header.Get("X-Thread-Id"))
	respID := "resp_" + strings.ReplaceAll(uuid(), "-", "")
	msgID := "msg_" + strings.ReplaceAll(uuid(), "-", "")
	created := time.Now().Unix()

	thinkingEffort := strings.TrimSpace(s.cfg.ThinkingEffort)
	if thinkingEffort == "" && req.Reasoning != nil {
		thinkingEffort = upstreamThinkingEffort(req.Reasoning.Effort)
	}

	in := ChatInput{
		Prompt:         promptText,
		Messages:       promptMessages,
		Model:          model,
		ThreadID:       threadID,
		Title:          titleFor(lastUserText(msgs)),
		SystemPrompt:   systemPromptText,
		ChatAgentID:    s.cfg.ChatAgentID,
		ThinkingEffort: thinkingEffort,
		CreateSession:  true,
		DeleteSession:  !s.cfg.SessionReuse && !s.cfg.KeepSessions,
	}
	if resume {
		in.ThreadID = conv.threadID
		in.CreateSession = false
		// 复用上游线程时只发送增量；系统指令已经在线程上下文里。
		in.SystemPrompt = ""
	}

	resetConversation := func() {
		conv.reset()
		resume = false
		in.ThreadID = ""
		in.CreateSession = true
		in.Prompt = prompt
		in.Messages = msgs
		in.SystemPrompt = systemPromptText
	}
	rememberConversation := func(acct *store.Account, res ChatResult) {
		if !s.cfg.SessionReuse || acct == nil || res.ThreadID == "" {
			return
		}
		conv.threadID = res.ThreadID
		conv.accountID = acct.ID
		conv.messages = append([]Message(nil), msgs...)
		conv.systemPrompt = systemPromptText
		conv.turns++
	}
	requestedModel := strings.TrimSpace(req.Model)
	if requestedModel == "" {
		requestedModel = "(default)"
	}
	log.Printf("responses %s start model=%s upstream_model=%s stream=%t messages=%d prompt_chars=%d system_chars=%d reuse=%t thread=%q",
		respID, requestedModel, model, req.Stream, len(msgs), len(promptText), len(systemPromptText), resume, in.ThreadID)

	if !req.Stream {
		var res ChatResult
		var acct *store.Account
		var lastErr error
		if resume {
			res, acct, lastErr = s.execNonStream(ctx, in, conv.accountID)
			if lastErr != nil {
				log.Printf("responses %s session resume failed; retrying with full transcript", respID)
				var retryAccountID int64
				if acct != nil {
					retryAccountID = acct.ID
				}
				resetConversation()
				res, acct, lastErr = s.execNonStreamPrefer(ctx, in, retryAccountID)
			}
		} else {
			res, acct, lastErr = s.execNonStream(ctx, in, 0)
		}
		if lastErr != nil {
			conv.reset()
			status, code := http.StatusBadGateway, "upstream_error"
			if errors.Is(lastErr, accounts.ErrNoAccounts) {
				status, code = http.StatusUnauthorized, "upstream_session_unavailable"
			}
			writeOpenAIError(w, status, code, lastErr.Error())
			return
		}
		rememberConversation(acct, res)
		text := res.Text
		var toolCalls []ToolCall
		if bridgeActive {
			var rerr error
			text, toolCalls, rerr = resolveToolCalls(res, bridgeAllowed, choiceMode, choiceName, parallelTools)
			if rerr != nil {
				conv.reset()
				code := "upstream_error"
				if errors.Is(rerr, errToolMarker) {
					code = "invalid_tool_call"
				}
				writeOpenAIError(w, http.StatusBadGateway, code, rerr.Error())
				return
			}
			markCustomCalls(toolCalls, customToolNames(bridgeDefs))
			toolCalls = applyToolOrigins(toolCalls, toolOrigins)
		}
		text, decisionCalls := extractDecisionCalls(text, decisionAllowed)
		toolCalls = append(toolCalls, decisionCalls...)
		if strings.TrimSpace(text) == "" && len(toolCalls) == 0 {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "upstream returned no text or tool calls")
			return
		}
		output := buildResponsesOutputItemsWithThinking(msgID, res.Thinking, text, toolCalls)
		writeJSON(w, http.StatusOK, buildResponseObject(
			respID, model, req.Instructions, "completed", created, output, responsesUsageMap(res.Usage),
		))
		return
	}

	// 串流模式：發送標準 OpenAI Responses API SSE 事件。
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming_unavailable", "response writer does not support flushing")
		return
	}

	started := false
	seq := 0
	writeSSE := func(eventType string, payload map[string]any) {
		payload["type"] = eventType
		payload["sequence_number"] = seq
		seq++
		body, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, body)
		flusher.Flush()
	}

	ensureStarted := func() {
		if started {
			return
		}
		started = true
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		if threadID != "" {
			w.Header().Set("X-Thread-Id", threadID)
		}
		w.WriteHeader(http.StatusOK)

		inProg := buildResponseObject(respID, model, req.Instructions, "in_progress", created, nil, nil)
		writeSSE("response.created", map[string]any{"response": inProg})
		writeSSE("response.in_progress", map[string]any{"response": inProg})
	}

	nextOutputIdx := 0
	textItemStarted := false
	textOutputIdx := -1
	var textBuf strings.Builder

	thinkingItemStarted := false
	thinkingItemID := ""
	thinkingOutputIdx := -1
	var thinkingBuf strings.Builder

	ensureThinkingItem := func() {
		if thinkingItemStarted {
			return
		}
		ensureStarted()
		thinkingItemStarted = true
		thinkingItemID = "rs_" + strings.ReplaceAll(uuid(), "-", "")
		thinkingOutputIdx = nextOutputIdx
		nextOutputIdx++
		writeSSE("response.output_item.added", map[string]any{
			"output_index": thinkingOutputIdx,
			"item": map[string]any{
				"id":      thinkingItemID,
				"type":    "reasoning",
				"summary": []any{},
			},
		})
		writeSSE("response.reasoning_summary_part.added", map[string]any{
			"item_id":       thinkingItemID,
			"output_index":  thinkingOutputIdx,
			"summary_index": 0,
			"part":          map[string]any{"type": "summary_text", "text": ""},
		})
	}

	closeThinkingItem := func() map[string]any {
		full := thinkingBuf.String()
		writeSSE("response.reasoning_summary_part.done", map[string]any{
			"item_id":       thinkingItemID,
			"output_index":  thinkingOutputIdx,
			"summary_index": 0,
			"part":          map[string]any{"type": "summary_text", "text": full},
		})
		item := buildReasoningItem(thinkingItemID, full)
		writeSSE("response.output_item.done", map[string]any{
			"output_index": thinkingOutputIdx,
			"item":         item,
		})
		return item
	}

	ensureTextItem := func() {
		if textItemStarted {
			return
		}
		ensureStarted()
		textItemStarted = true
		textOutputIdx = nextOutputIdx
		nextOutputIdx++
		writeSSE("response.output_item.added", map[string]any{
			"output_index": textOutputIdx,
			"item": map[string]any{
				"id":      msgID,
				"type":    "message",
				"status":  "in_progress",
				"role":    "assistant",
				"content": []any{},
			},
		})
		writeSSE("response.content_part.added", map[string]any{
			"item_id":       msgID,
			"output_index":  textOutputIdx,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})
	}

	closeTextItem := func() map[string]any {
		fullText := textBuf.String()
		writeSSE("response.output_text.done", map[string]any{
			"item_id":       msgID,
			"output_index":  textOutputIdx,
			"content_index": 0,
			"text":          fullText,
		})
		part := map[string]any{
			"type":        "output_text",
			"text":        fullText,
			"annotations": []any{},
		}
		writeSSE("response.content_part.done", map[string]any{
			"item_id":       msgID,
			"output_index":  textOutputIdx,
			"content_index": 0,
			"part":          part,
		})
		item := map[string]any{
			"id":      msgID,
			"type":    "message",
			"status":  "completed",
			"role":    "assistant",
			"content": []map[string]any{part},
		}
		writeSSE("response.output_item.done", map[string]any{
			"output_index": textOutputIdx,
			"item":         item,
		})
		return item
	}

	type streamToolState struct {
		fcID      string
		callID    string
		name      string
		outputIdx int
		args      string
		custom    bool
	}
	customNames := customToolNames(bridgeDefs)
	toolMap := make(map[string]*streamToolState)
	var toolOrder []string
	var bridgeBuffer strings.Builder

	emitNativeTool := func(tc ToolCallState) {
		if !toolAllowed(tc, allowedTools, choiceMode, choiceName) {
			return
		}
		st, seen := toolMap[tc.ID]
		if !seen {
			if !parallelTools && len(toolOrder) > 0 {
				return
			}
			ensureStarted()
			fcID := "fc_" + strings.ReplaceAll(uuid(), "-", "")
			idx := nextOutputIdx
			nextOutputIdx++
			st = &streamToolState{
				fcID:      fcID,
				callID:    tc.ID,
				name:      tc.Name,
				outputIdx: idx,
				custom:    customNames[tc.Name],
			}
			toolMap[tc.ID] = st
			toolOrder = append(toolOrder, tc.ID)
			addedItem := map[string]any{
				"id":        fcID,
				"type":      "function_call",
				"status":    "in_progress",
				"call_id":   tc.ID,
				"name":      tc.Name,
				"arguments": "",
			}
			if st.custom {
				addedItem["type"] = "custom_tool_call"
				addedItem["input"] = ""
				delete(addedItem, "arguments")
			}
			writeSSE("response.output_item.added", map[string]any{
				"output_index": idx,
				"item":         addedItem,
			})
		}
		args := strings.TrimSpace(tc.Arguments)
		if args == "" {
			args = "{}"
		}
		delta := args
		if strings.HasPrefix(args, st.args) {
			delta = args[len(st.args):]
		}
		st.args = args
		if delta != "" && !st.custom {
			writeSSE("response.function_call_arguments.delta", map[string]any{
				"item_id":      st.fcID,
				"output_index": st.outputIdx,
				"delta":        delta,
			})
		}
	}

	var res ChatResult
	var lastErr error
	var usedAccount *store.Account
	pinnedFirst := resume && conv.accountID != 0
	var preferredAccount int64
	for range s.maxAttempts() {
		var acct *store.Account
		var err error
		if pinnedFirst || preferredAccount != 0 {
			accountID := preferredAccount
			preferredAccount = 0
			if pinnedFirst {
				accountID = conv.accountID
				pinnedFirst = false
			}
			acct, err = s.accounts.NextPinned(ctx, accountID)
			if err != nil {
				log.Printf("responses %s preferred account %d unavailable; selecting another account", respID, accountID)
				resetConversation()
				acct, err = s.accounts.Next(ctx)
			}
		} else {
			acct, err = s.accounts.Next(ctx)
		}
		if err != nil {
			lastErr = err
			break
		}
		sess := SessionFromAccount(acct, s.cfg)
		if err := sess.validate(); err != nil {
			s.accounts.ReportFailure(ctx, acct.ID, err)
			lastErr = err
			if resume {
				resetConversation()
			}
			continue
		}
		res, err = s.chatChunked(ctx, sess, in, func(ev StreamEvent) error {
			switch ev.Type {
			case "thinking":
				if ev.Text == "" {
					return nil
				}
				ensureThinkingItem()
				thinkingBuf.WriteString(ev.Text)
				writeSSE("response.reasoning_summary_text.delta", map[string]any{
					"item_id":       thinkingItemID,
					"output_index":  thinkingOutputIdx,
					"summary_index": 0,
					"delta":         ev.Text,
				})
			case "text":
				if ev.Text == "" {
					return nil
				}
				if bridgeActive {
					bridgeBuffer.WriteString(ev.Text)
					return nil
				}
				ensureTextItem()
				textBuf.WriteString(ev.Text)
				writeSSE("response.output_text.delta", map[string]any{
					"item_id":       msgID,
					"output_index":  textOutputIdx,
					"content_index": 0,
					"delta":         ev.Text,
				})
			case "tool_call", "tool_call_update":
				if bridgeActive {
					return nil
				}
				if ev.ToolCall != nil {
					emitNativeTool(*ev.ToolCall)
				}
			}
			return nil
		})
		if err == nil {
			lastErr = nil
			usedAccount = acct
			break
		}
		lastErr = err
		s.accounts.ReportFailure(ctx, acct.ID, err)
		if started || ctx.Err() != nil {
			break
		}
		wasResume := resume
		thinkingBuf.Reset()
		resetConversation()
		if wasResume {
			preferredAccount = acct.ID
			log.Printf("responses %s session resume failed; retrying with full transcript", respID)
		}
		// 尚未輸出給客戶端前可安全重置並換號重試。
		nextOutputIdx = 0
		textItemStarted = false
		textOutputIdx = -1
		textBuf.Reset()
		bridgeBuffer.Reset()
		toolMap = make(map[string]*streamToolState)
		toolOrder = nil
	}

	if lastErr != nil {
		conv.reset()
		if !started {
			status, code := http.StatusBadGateway, "upstream_error"
			if errors.Is(lastErr, accounts.ErrNoAccounts) {
				status, code = http.StatusUnauthorized, "upstream_session_unavailable"
			}
			writeOpenAIError(w, status, code, lastErr.Error())
			return
		}
		writeSSE("error", map[string]any{
			"code":    "upstream_error",
			"message": lastErr.Error(),
		})
		return
	}
	rememberConversation(usedAccount, res)

	var completedItems []map[string]any
	if thinkingItemStarted {
		closeThinkingItem()
		completedItems = append(completedItems, buildReasoningItem(thinkingItemID, thinkingBuf.String()))
	}
	if bridgeActive {
		var rerr error
		text, toolCalls, rerr := resolveToolCalls(ChatResult{Text: bridgeBuffer.String(), ToolCalls: res.ToolCalls}, bridgeAllowed, choiceMode, choiceName, parallelTools)
		if rerr != nil {
			conv.reset()
			code := "upstream_error"
			if errors.Is(rerr, errToolMarker) {
				code = "invalid_tool_call"
			}
			if !started {
				writeOpenAIError(w, http.StatusBadGateway, code, rerr.Error())
				return
			}
			writeSSE("error", map[string]any{"code": code, "message": rerr.Error()})
			return
		}
		markCustomCalls(toolCalls, customToolNames(bridgeDefs))
		toolCalls = applyToolOrigins(toolCalls, toolOrigins)
		text, decisionCalls := extractDecisionCalls(text, decisionAllowed)
		toolCalls = append(toolCalls, decisionCalls...)
		replies := toolCalls
		if strings.TrimSpace(text) == "" && len(replies) == 0 {
			if !started {
				writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "upstream returned no text or tool calls")
				return
			}
			writeSSE("error", map[string]any{
				"code":    "upstream_error",
				"message": "upstream returned no text or tool calls",
			})
			return
		}
		ensureStarted()
		if strings.TrimSpace(text) != "" {
			ensureTextItem()
			textBuf.WriteString(text)
			writeSSE("response.output_text.delta", map[string]any{
				"item_id":       msgID,
				"output_index":  textOutputIdx,
				"content_index": 0,
				"delta":         text,
			})
			completedItems = append(completedItems, closeTextItem())
		}
		for _, tc := range replies {
			fcID := "fc_" + strings.ReplaceAll(uuid(), "-", "")
			idx := nextOutputIdx
			nextOutputIdx++
			args := strings.TrimSpace(tc.Function.Arguments)
			if args == "" {
				args = "{}"
			}
			if tc.Custom {
				input := customToolInput(args)
				addedItem := map[string]any{
					"id":      fcID,
					"type":    "custom_tool_call",
					"status":  "in_progress",
					"call_id": tc.ID,
					"name":    tc.Function.Name,
					"input":   "",
				}
				if tc.Namespace != "" {
					addedItem["namespace"] = tc.Namespace
				}
				writeSSE("response.output_item.added", map[string]any{"output_index": idx, "item": addedItem})
				writeSSE("response.custom_tool_call_input.delta", map[string]any{
					"item_id":      fcID,
					"output_index": idx,
					"delta":        input,
				})
				writeSSE("response.custom_tool_call_input.done", map[string]any{
					"item_id":      fcID,
					"output_index": idx,
					"input":        input,
				})
				doneItem := map[string]any{
					"id":      fcID,
					"type":    "custom_tool_call",
					"status":  "completed",
					"call_id": tc.ID,
					"name":    tc.Function.Name,
					"input":   input,
				}
				if tc.Namespace != "" {
					doneItem["namespace"] = tc.Namespace
				}
				writeSSE("response.output_item.done", map[string]any{"output_index": idx, "item": doneItem})
				completedItems = append(completedItems, doneItem)
				continue
			}
			addedItem := map[string]any{
				"id":        fcID,
				"type":      "function_call",
				"status":    "in_progress",
				"call_id":   tc.ID,
				"name":      tc.Function.Name,
				"arguments": "",
			}
			if tc.Namespace != "" {
				addedItem["namespace"] = tc.Namespace
			}
			writeSSE("response.output_item.added", map[string]any{
				"output_index": idx,
				"item":         addedItem,
			})
			writeSSE("response.function_call_arguments.delta", map[string]any{
				"item_id":      fcID,
				"output_index": idx,
				"delta":        args,
			})
			writeSSE("response.function_call_arguments.done", map[string]any{
				"item_id":      fcID,
				"output_index": idx,
				"arguments":    args,
			})
			fcItem := map[string]any{
				"id":        fcID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   tc.ID,
				"name":      tc.Function.Name,
				"arguments": args,
			}
			if tc.Namespace != "" {
				fcItem["namespace"] = tc.Namespace
			}
			writeSSE("response.output_item.done", map[string]any{
				"output_index": idx,
				"item":         fcItem,
			})
			completedItems = append(completedItems, fcItem)
		}
	} else {
		if !textItemStarted && len(toolOrder) == 0 {
			if !started {
				writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "upstream returned no text or tool calls")
				return
			}
			writeSSE("error", map[string]any{
				"code":    "upstream_error",
				"message": "upstream returned no text or tool calls",
			})
			return
		}
		if textItemStarted {
			completedItems = append(completedItems, closeTextItem())
		}
		for _, id := range toolOrder {
			st := toolMap[id]
			if st.custom {
				input := customToolInput(st.args)
				writeSSE("response.custom_tool_call_input.delta", map[string]any{
					"item_id":      st.fcID,
					"output_index": st.outputIdx,
					"delta":        input,
				})
				writeSSE("response.custom_tool_call_input.done", map[string]any{
					"item_id":      st.fcID,
					"output_index": st.outputIdx,
					"input":        input,
				})
				item := map[string]any{
					"id":      st.fcID,
					"type":    "custom_tool_call",
					"status":  "completed",
					"call_id": st.callID,
					"name":    st.name,
					"input":   input,
				}
				writeSSE("response.output_item.done", map[string]any{
					"output_index": st.outputIdx,
					"item":         item,
				})
				completedItems = append(completedItems, item)
				continue
			}
			writeSSE("response.function_call_arguments.done", map[string]any{
				"item_id":      st.fcID,
				"output_index": st.outputIdx,
				"arguments":    st.args,
			})
			fcItem := map[string]any{
				"id":        st.fcID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   st.callID,
				"name":      st.name,
				"arguments": st.args,
			}
			writeSSE("response.output_item.done", map[string]any{
				"output_index": st.outputIdx,
				"item":         fcItem,
			})
			completedItems = append(completedItems, fcItem)
		}
	}

	completedResp := buildResponseObject(
		respID, model, req.Instructions, "completed", created, completedItems, responsesUsageMap(res.Usage),
	)
	writeSSE("response.completed", map[string]any{"response": completedResp})
}

func toolAllowed(tc ToolCallState, allowed map[string]bool, mode, name string) bool {
	if mode == "none" || len(allowed) == 0 {
		return false
	}
	if !allowed[tc.Name] {
		return false
	}
	if mode == "function" && name != "" && tc.Name != name {
		return false
	}
	return true
}
