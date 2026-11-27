package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxReuseTurns 限制同一上游线程连续复用的轮数。上游会话约 30 分钟过期，
// 定期全量重发可避免复用已被服务端裁剪或即将过期的历史。
const maxReuseTurns = 10

// conversation keeps the upstream thread used for one client conversation and
// the client messages already forwarded to it.
type conversation struct {
	mu           sync.Mutex
	threadID     string
	accountID    int64
	messages     []Message
	systemPrompt string
	turns        int
	lastUsed     atomic.Int64
}

func (c *conversation) touch() {
	c.lastUsed.Store(time.Now().UnixNano())
}

func (c *conversation) idle() time.Duration {
	n := c.lastUsed.Load()
	if n == 0 {
		return 0
	}
	return time.Since(time.Unix(0, n))
}

func (c *conversation) reset() {
	c.threadID = ""
	c.accountID = 0
	c.messages = nil
	c.systemPrompt = ""
	c.turns = 0
}

// delta returns the messages added since the last forwarded turn. A false
// result means the history was edited or there is nothing new, so the caller
// must start a fresh upstream session with the full transcript.
func (c *conversation) delta(msgs []Message) ([]Message, bool) {
	if c.threadID == "" || len(c.messages) == 0 {
		return nil, false
	}
	if len(msgs) <= len(c.messages) {
		return nil, false
	}
	for i := range c.messages {
		if !reflect.DeepEqual(c.messages[i], msgs[i]) {
			return nil, false
		}
	}
	return msgs[len(c.messages):], true
}

type conversationStore struct {
	mu      sync.Mutex
	entries map[string]*conversation
	ttl     time.Duration
	max     int
}

func newConversationStore(ttl time.Duration, max int) *conversationStore {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if max < 1 {
		max = 256
	}
	return &conversationStore{entries: make(map[string]*conversation), ttl: ttl, max: max}
}

// acquire locks one conversation for the duration of a request. Expired and
// least-recently-used conversations are dropped first. The store lock is
// released before waiting on a conversation so one busy chat cannot stall
// unrelated conversations.
func (s *conversationStore) acquire(key string) *conversation {
	s.mu.Lock()
	for k, c := range s.entries {
		if c.idle() > s.ttl {
			delete(s.entries, k)
		}
	}
	if len(s.entries) >= s.max {
		var oldestKey string
		var oldest time.Time
		for k, c := range s.entries {
			used := time.Unix(0, c.lastUsed.Load())
			if oldestKey == "" || used.Before(oldest) {
				oldestKey, oldest = k, used
			}
		}
		if oldestKey != "" {
			delete(s.entries, oldestKey)
		}
	}
	c := s.entries[key]
	if c == nil {
		c = &conversation{}
		s.entries[key] = c
	}
	s.mu.Unlock()

	c.mu.Lock()
	c.touch()
	return c
}

func (s *conversationStore) release(c *conversation) {
	c.touch()
	c.mu.Unlock()
}

// conversationKey identifies one OpenAI conversation. A client-provided
// X-Conversation-Id or X-Thread-Id wins; otherwise the full system prompt
// (including tool definitions) plus the first user message is used so the key
// stays stable while the transcript grows.
func conversationKey(r *http.Request, req *ChatRequest, fullSystemPrompt string) string {
	for _, header := range []string{"X-Conversation-Id", "X-Thread-Id"} {
		if id := strings.TrimSpace(r.Header.Get(header)); id != "" {
			return "h:" + id
		}
	}
	h := sha256.New()
	h.Write([]byte(r.Header.Get("Authorization")))
	h.Write([]byte{0})
	h.Write([]byte(r.Header.Get("X-Api-Key")))
	h.Write([]byte{0})
	h.Write([]byte(fullSystemPrompt))
	for _, m := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "system" || role == "developer" {
			continue
		}
		h.Write([]byte{0})
		h.Write([]byte(role))
		h.Write([]byte{0})
		h.Write([]byte(contentText(m.Content)))
		return "c:" + hex.EncodeToString(h.Sum(nil))
	}
	for _, m := range req.Messages {
		h.Write([]byte{0})
		h.Write([]byte(m.Role))
		h.Write([]byte(contentText(m.Content)))
	}
	return "c:" + hex.EncodeToString(h.Sum(nil))
}

// responsesConversationKey 识别 Responses API 调用所属的会话。客户端显式
// 提供的 prompt_cache_key 最可靠；opencode 等客户端即使没有开启
// prompt_cache_key，也会在每一轮带上会话头，因此一并识别；最后才退回
// “鉴权信息 + 系统指令 + 首条非系统消息”的哈希。
func responsesConversationKey(r *http.Request, promptCacheKey string, msgs []Message, fullSystemPrompt string) string {
	if key := strings.TrimSpace(promptCacheKey); key != "" {
		return "rp:" + key
	}
	for _, header := range []string{
		"X-Conversation-Id",
		"X-Thread-Id",
		"X-Opencode-Session",
		"X-Session-Affinity",
		"X-Session-Id",
		"X-Opencode-Session-Id",
	} {
		if id := strings.TrimSpace(r.Header.Get(header)); id != "" {
			return "rh:" + id
		}
	}
	h := sha256.New()
	h.Write([]byte(r.Header.Get("Authorization")))
	h.Write([]byte{0})
	h.Write([]byte(r.Header.Get("X-Api-Key")))
	h.Write([]byte{0})
	h.Write([]byte(fullSystemPrompt))
	for _, m := range msgs {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "system" || role == "developer" {
			continue
		}
		h.Write([]byte{0})
		h.Write([]byte(role))
		h.Write([]byte{0})
		h.Write([]byte(contentText(m.Content)))
		return "rc:" + hex.EncodeToString(h.Sum(nil))
	}
	for _, m := range msgs {
		h.Write([]byte{0})
		h.Write([]byte(m.Role))
		h.Write([]byte(contentText(m.Content)))
	}
	return "rc:" + hex.EncodeToString(h.Sum(nil))
}
