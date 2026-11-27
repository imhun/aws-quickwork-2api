package proxy

import (
	"context"
	"fmt"
	"log"
	"strings"
)

const relayChunkPlaceholderUUID = "00000000-0000-4000-8000-000000000000"

// chatChunked 在 relay 文本超过上游单帧上限时按连接逐段发送。上游一个
// WebSocket 连接只处理一轮，所以每段都重连一次，并用同一个 thread 复用会话：
// 前面的分段只作为上下文，最后一段承接用户请求。这样调用方指令始终以
// 消息正文的身份出现，不会被上游当成附件资料。
func (s *Server) chatChunked(ctx context.Context, sess *Session, in ChatInput, onEvent EventFunc) (ChatResult, error) {
	relay := relayPrompt(in.SystemPrompt, in.Prompt)
	if isGatewayProvider(sess.Provider) || strings.TrimSpace(sess.TenantURL) == "" {
		return s.chat(ctx, sess, in, onEvent)
	}

	threadID := strings.TrimSpace(in.ThreadID)
	budgetThreadID := threadID
	if budgetThreadID == "" {
		budgetThreadID = relayChunkPlaceholderUUID
	}
	template := buildUserMessagePayload(s.cfg, sess, in, budgetThreadID,
		relayChunkPlaceholderUUID, relayChunkPlaceholderUUID, "")
	if fits, err := frameTextFits(template, relay); err != nil {
		return ChatResult{}, err
	} else if fits {
		return s.chat(ctx, sess, in, onEvent)
	}

	// 超长时只切对话正文，把工具说明放到最后一段紧贴任务。否则模型在第 1 段
	// 读到工具、第 5 段才看到任务，容易认为工具协议“不生效”。
	systemBlock := ""
	if strings.TrimSpace(in.SystemPrompt) != "" {
		systemBlock = in.SystemPrompt + "\n\n---\n\n"
	}
	reserve := jsonStringBytes(systemBlock)
	prefix := relayFrameHeader(relayChunkMaxTotal, relayChunkMaxTotal)
	chunks, err := splitRelayTextForFrame(in.Prompt, prefix, template, reserve)
	if err != nil || len(chunks) == 0 {
		// 工具说明本身就放不进一帧时退回整段切分。
		systemBlock = ""
		reserve = 0
		chunks, err = splitRelayTextForFrame(relay, prefix, template, reserve)
		if err != nil {
			return ChatResult{}, fmt.Errorf("split relay: %w", err)
		}
	}
	maxFrame := 0
	for i, chunk := range chunks {
		text := relayFrameHeader(i+1, len(chunks)) + chunk
		probe := buildUserMessagePayload(s.cfg, sess, in, budgetThreadID,
			relayChunkPlaceholderUUID, relayChunkPlaceholderUUID, text)
		n, err := userMessageBytes(probe)
		if err != nil {
			return ChatResult{}, err
		}
		if n > maxUserMessageBytes {
			return ChatResult{}, fmt.Errorf("relay chunk %d/%d frame %d bytes: %w", i+1, len(chunks), n, ErrUserMessageTooLarge)
		}
		maxFrame = max(maxFrame, n)
	}

	var result ChatResult
	for i, chunk := range chunks {
		text := relayFrameHeader(i+1, len(chunks))
		if i == len(chunks)-1 {
			text += systemBlock
		}
		part := in
		part.SystemPrompt = ""
		part.Prompt = text + chunk
		part.ThreadID = threadID
		part.CreateSession = threadID == ""
		part.DeleteSession = in.DeleteSession && i == len(chunks)-1
		part.RelayChunk = true
		var emit EventFunc
		if i == len(chunks)-1 {
			emit = onEvent
		}
		res, err := s.chat(ctx, sess, part, emit)
		if err != nil {
			return res, fmt.Errorf("relay chunk %d/%d: %w", i+1, len(chunks), err)
		}
		result = res
		threadID = res.ThreadID
	}
	log.Printf("relay chunked thread=%s chunks=%d bytes=%d max_frame=%d", threadID, len(chunks), len(relay), maxFrame)
	return result, nil
}
