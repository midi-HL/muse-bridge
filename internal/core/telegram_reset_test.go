package core

import (
	"context"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"testing"
)

func TestSessionResetInvalidatesTelegramRetryState(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.TelegramSession(ctx, 42, 7); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberTelegramPrompt(ctx, "telegram_42", "7", "erase me"); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(ctx, "session", "telegram_42", sessionRecord{ID: "telegram_42", Platform: "telegram", UserID: "42", Messages: []hatch.Message{{Role: "user", Content: "erase me"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "sessions.reset", map[string]any{"id": "telegram_42"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TelegramLastPrompt(ctx, "telegram_42", "7"); err == nil {
		t.Fatal("retry prompt survived session reset")
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "telegram_42", Platform: "telegram", UserID: "42", RetryUserID: "7", Retry: true}, nil); err == nil {
		t.Fatal("retry resurrected a reset session")
	}
}
