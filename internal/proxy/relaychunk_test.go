package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"quickwork-openai/internal/config"
)

func relayTestPayload() map[string]any {
	cfg := config.Default()
	sess := &Session{Surface: "QUICK_WORK_DESKTOP_WINDOWS", OutpostID: "outpost-123"}
	in := ChatInput{Model: "aws-quick/smart", ThinkingEffort: "high", ChatAgentID: "agent-1"}
	return buildUserMessagePayload(&cfg, sess, in, relayChunkPlaceholderUUID,
		relayChunkPlaceholderUUID, relayChunkPlaceholderUUID, "")
}

func TestJSONStringBytesMatchesEncodingJSON(t *testing.T) {
	text := "plain 中文 😀\n\t\"quoted\" \\ <tag> & value \u2028 \u2029 \x01" + string([]byte{0xff})
	body, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := jsonStringBytes(text), len(body)-2; got != want {
		t.Fatalf("jsonStringBytes mismatch: got %d want %d", got, want)
	}
	byRune := 0
	for rest := text; len(rest) > 0; {
		size, encoded := jsonRuneCost(rest)
		byRune += encoded
		rest = rest[size:]
	}
	if got, want := byRune, jsonStringBytes(text); got != want {
		t.Fatalf("jsonRuneCost mismatch: got %d want %d", got, want)
	}
}

func TestFrameTextFitsCountsJSONEscaping(t *testing.T) {
	payload := relayTestPayload()
	fits, err := frameTextFits(payload, strings.Repeat("&", maxUserMessageBytes))
	if err != nil {
		t.Fatal(err)
	}
	if fits {
		t.Fatal("HTML characters must be counted by their escaped JSON size")
	}
}

func TestSplitRelayTextForFrameKeepsContentAndLimit(t *testing.T) {
	payload := relayTestPayload()
	unit := "line <&> \"quoted\" \\ 中文\n"
	text := strings.Repeat(unit, 4000)
	prefix := relayFrameHeader(relayChunkMaxTotal, relayChunkMaxTotal)
	chunks, err := splitRelayTextForFrame(text, prefix, payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	if joined := strings.Join(chunks, ""); joined != text {
		t.Fatalf("chunks lost content: got %d bytes want %d", len(joined), len(text))
	}
	for i, chunk := range chunks {
		if chunk == "" {
			t.Fatalf("chunk %d is empty", i)
		}
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
		text := relayFrameHeader(i+1, len(chunks)) + chunk
		probe := clonePayload(payload)
		probe["text"] = text
		n, err := userMessageBytes(probe)
		if err != nil {
			t.Fatal(err)
		}
		if n > maxUserMessageBytes {
			t.Fatalf("chunk %d frame is %d bytes, over limit %d", i, n, maxUserMessageBytes)
		}
	}
}

func TestChatChunkedUsesInlineFramesForEscapedText(t *testing.T) {
	cfg := config.Default()
	var calls []ChatInput
	server := &Server{cfg: &cfg}
	server.chat = func(_ context.Context, _ *Session, in ChatInput, _ EventFunc) (ChatResult, error) {
		calls = append(calls, in)
		return ChatResult{ThreadID: "thread-1"}, nil
	}
	sess := &Session{Provider: "social", TenantURL: "https://tenant.example"}
	in := ChatInput{Prompt: strings.Repeat("&", 60000), Model: "aws-quick/smart"}
	if _, err := server.chatChunked(context.Background(), sess, in, nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 2 {
		t.Fatalf("expected inline chunking, got %d call(s)", len(calls))
	}
	payload := relayTestPayload()
	for i, call := range calls {
		if !call.RelayChunk {
			t.Fatalf("call %d did not mark RelayChunk", i)
		}
		if call.SystemPrompt != "" {
			t.Fatalf("call %d repeated system prompt", i)
		}
		probe := clonePayload(payload)
		probe["text"] = relayPrompt(call.SystemPrompt, call.Prompt)
		n, err := userMessageBytes(probe)
		if err != nil {
			t.Fatal(err)
		}
		if n > maxUserMessageBytes {
			t.Fatalf("call %d frame is %d bytes, over limit %d", i, n, maxUserMessageBytes)
		}
	}
}

func TestRelayFrameHeader(t *testing.T) {
	middle := relayFrameHeader(1, 3)
	wantMiddle := "（长消息第 1/3 段，尚未结束。请只回复 ACK，等最后一段到齐再处理。）\n\n"
	if middle != wantMiddle {
		t.Fatalf("middle header mismatch:\n got %q\nwant %q", middle, wantMiddle)
	}
	final := relayFrameHeader(3, 3)
	wantFinal := "（长消息第 3/3 段，消息到此完整。）\n\n"
	if final != wantFinal {
		t.Fatalf("final header mismatch:\n got %q\nwant %q", final, wantFinal)
	}
	if got := relayFrameHeader(1, 1); got != "" {
		t.Fatalf("single part header should be empty, got %q", got)
	}
}

func TestChatChunkedPutsToolBlockInLastFrame(t *testing.T) {
	cfg := config.Default()
	var calls []ChatInput
	server := &Server{cfg: &cfg}
	server.chat = func(_ context.Context, _ *Session, in ChatInput, _ EventFunc) (ChatResult, error) {
		calls = append(calls, in)
		return ChatResult{ThreadID: "thread-1"}, nil
	}
	sess := &Session{Provider: "social", TenantURL: "https://tenant.example"}
	system := "<available_tools>\nexec_command\n</available_tools>"
	in := ChatInput{Prompt: strings.Repeat("上下文行，检查通过。\n", 8000), SystemPrompt: system, Model: "aws-quick/smart"}
	if _, err := server.chatChunked(context.Background(), sess, in, nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 2 {
		t.Fatalf("expected multiple frames, got %d", len(calls))
	}
	for i, call := range calls {
		last := i == len(calls)-1
		hasBlock := strings.Contains(call.Prompt, "<available_tools>")
		if last && !hasBlock {
			t.Fatalf("final frame missing tool block")
		}
		if !last && hasBlock {
			t.Fatalf("frame %d should not repeat tool block", i)
		}
		if call.SystemPrompt != "" {
			t.Fatalf("frame %d repeated SystemPrompt", i)
		}
	}
}
