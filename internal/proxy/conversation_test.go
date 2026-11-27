package proxy

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestResponsesConversationKeyPrefersPromptCacheKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("X-Session-Id", "session-header")
	req.Header.Set("X-Conversation-Id", "conversation-header")
	msgs := []Message{{Role: "user", Content: "hello"}}

	got := responsesConversationKey(req, "cache-key", msgs, "system")
	want := "rp:cache-key"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestResponsesConversationKeyUsesSessionHeaders(t *testing.T) {
	headers := []string{
		"X-Conversation-Id",
		"X-Thread-Id",
		"X-Opencode-Session",
		"X-Session-Affinity",
		"X-Session-Id",
		"X-Opencode-Session-Id",
	}
	msgs := []Message{{Role: "user", Content: "hello"}}
	for _, header := range headers {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set(header, "session-1")
		got := responsesConversationKey(req, "", msgs, "system")
		want := "rh:session-1"
		if got != want {
			t.Fatalf("header %s: expected %q, got %q", header, want, got)
		}
	}
}

func TestResponsesConversationKeyFallbackStaysStable(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer key-a")
	req.Header.Set("X-Api-Key", "key-a")
	first := []Message{
		{Role: "system", Content: "be concise"},
		{Role: "user", Content: "first task"},
	}
	second := append(append([]Message(nil), first...),
		Message{Role: "assistant", Content: "working"},
		Message{Role: "user", Content: "follow up"},
	)

	got := responsesConversationKey(req, "", second, "be concise")
	want := responsesConversationKey(req, "", first, "be concise")
	if got != want {
		t.Fatalf("expected the growing transcript to keep key %q, got %q", want, got)
	}

	other := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	other.Header.Set("Authorization", "Bearer key-b")
	other.Header.Set("X-Api-Key", "key-b")
	if otherKey := responsesConversationKey(other, "", first, "be concise"); otherKey == want {
		t.Fatalf("expected a different key for another credential, got %q", otherKey)
	}
}

func TestConversationDeltaRequiresUnchangedHistory(t *testing.T) {
	forwarded := []Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
	}
	conv := &conversation{threadID: "thread-1", messages: append([]Message(nil), forwarded...)}

	delta, ok := conv.delta(append(append([]Message(nil), forwarded...), Message{Role: "user", Content: "three"}))
	if !ok {
		t.Fatal("expected a usable delta for appended history")
	}
	want := []Message{{Role: "user", Content: "three"}}
	if !reflect.DeepEqual(delta, want) {
		t.Fatalf("expected delta %#v, got %#v", want, delta)
	}

	if _, ok := conv.delta([]Message{{Role: "user", Content: "one"}}); ok {
		t.Fatal("expected a shorter transcript to reject reuse")
	}
	if _, ok := conv.delta([]Message{{Role: "user", Content: "changed"}, {Role: "assistant", Content: "two"}, {Role: "user", Content: "three"}}); ok {
		t.Fatal("expected edited history to reject reuse")
	}
}

func TestConversationResetClearsReuseState(t *testing.T) {
	conv := &conversation{
		threadID:     "thread-1",
		accountID:    7,
		messages:     []Message{{Role: "user", Content: "one"}},
		systemPrompt: "system",
		turns:        maxReuseTurns,
	}
	conv.reset()
	if conv.threadID != "" || conv.accountID != 0 || conv.messages != nil || conv.systemPrompt != "" || conv.turns != 0 {
		t.Fatalf("reset left reuse state behind: %#v", conv)
	}
}
