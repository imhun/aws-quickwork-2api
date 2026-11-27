package proxy

import (
	"fmt"
	"unicode/utf8"
)

const (
	// relayChunkSafetyBytes 留出少量余量，覆盖上游回填 thread_id 等运行时字段
	// 与预估时使用占位值之间的差异。
	relayChunkSafetyBytes = 512
	// relayChunkMaxTotal 只用于生成足够长的分段说明，避免按实际总段数反复重算。
	relayChunkMaxTotal = 999999
)

// relayFrameHeader 返回分段 user_message 的说明文字。只做分页标记：不要把
// 前面的分段说成“调用方系统指令”，否则会被上游当成提示注入。
func relayFrameHeader(index, total int) string {
	if total <= 1 {
		return ""
	}
	if index < total {
		return fmt.Sprintf("（长消息第 %d/%d 段，尚未结束。请只回复 ACK，等最后一段到齐再处理。）\n\n", index, total)
	}
	return fmt.Sprintf("（长消息第 %d/%d 段，消息到此完整。）\n\n", index, total)
}

// frameTextBudget 返回某个 user_message 帧里 text 内容还能使用的 JSON 编码
// 字节数。payload 是除 text 内容外的真实字段模板，prefix 是每段固定说明。
func frameTextBudget(payload map[string]any, prefix string) (int, error) {
	probe := clonePayload(payload)
	probe["text"] = ""
	base, err := userMessageBytes(probe)
	if err != nil {
		return 0, err
	}
	room := maxUserMessageBytes - base - relayChunkSafetyBytes
	prefixBytes := jsonStringBytes(prefix)
	if room <= prefixBytes {
		return 0, fmt.Errorf("relay chunk prefix needs %d bytes, only %d available: %w", prefixBytes, room, ErrUserMessageTooLarge)
	}
	return room - prefixBytes, nil
}

// frameTextFits 判断完整 text 是否能放进一帧。这里使用真实 JSON 编码长度，
// 避免只按原始字符串长度估算而漏算换行、引号和 HTML 字符的转义膨胀。
func frameTextFits(payload map[string]any, text string) (bool, error) {
	probe := clonePayload(payload)
	probe["text"] = text
	n, err := userMessageBytes(probe)
	if err != nil {
		return false, err
	}
	return n <= maxUserMessageBytes, nil
}

// splitRelayTextForFrame 按真实 user_message 帧大小切分文本。prefix 使用比实际
// 分段更长的保守说明，payload 是不含 text 的字段模板。reserveBytes 预留给
// 最后一段追加的工具说明，避免它和正文一起超出单帧上限。
func splitRelayTextForFrame(text, prefix string, payload map[string]any, reserveBytes int) ([]string, error) {
	budget, err := frameTextBudget(payload, prefix)
	if err != nil {
		return nil, err
	}
	budget -= reserveBytes
	if budget <= 0 {
		return nil, fmt.Errorf("relay chunk reserve %d leaves no room: %w", reserveBytes, ErrUserMessageTooLarge)
	}
	chunks := make([]string, 0, len(text)/budget+1)
	rest := text
	for len(rest) > 0 {
		cut, err := nextFrameChunk(rest, budget)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, rest[:cut])
		rest = rest[cut:]
	}
	return chunks, nil
}

// nextFrameChunk 返回当前段应消费的原始字节数，优先在靠后的换行处断开。
func nextFrameChunk(text string, budget int) (int, error) {
	consumed := 0
	cost := 0
	lastNewline := 0
	for consumed < len(text) {
		size, encoded := jsonRuneCost(text[consumed:])
		if cost+encoded > budget {
			break
		}
		cost += encoded
		consumed += size
		if text[consumed-1] == '\n' && cost*10 >= budget*7 {
			lastNewline = consumed
		}
	}
	if consumed == 0 {
		return 0, fmt.Errorf("relay chunk cannot fit one character in %d bytes: %w", budget, ErrUserMessageTooLarge)
	}
	if lastNewline > 0 {
		return lastNewline, nil
	}
	return consumed, nil
}

// jsonRuneCost 返回下一个字符的原始字节数和 encoding/json 编码后的字节数。
func jsonRuneCost(s string) (size, encoded int) {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 1 {
		// encoding/json 会把非法 UTF-8 字节写成 U+FFFD（3 字节）。
		return 1, 3
	}
	switch r {
	case '\\', '"', '\n', '\r', '\t':
		return size, 2
	case '<', '>', '&', '\u2028', '\u2029':
		return size, 6
	}
	if r < 0x20 {
		return size, 6
	}
	return size, utf8.RuneLen(r)
}

func clonePayload(payload map[string]any) map[string]any {
	probe := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		probe[k] = v
	}
	return probe
}
