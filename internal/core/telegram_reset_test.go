package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func TestSessionResetConflictsWhileTelegramUpdateIsPending(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.TelegramSession(ctx, 42, 7); err != nil {
		t.Fatal(err)
	}
	pending := telegramChatRecord{ChatID: 42, UpdateID: 91, MessageID: 9, UserID: 7, SessionID: "telegram_42", Status: "pending", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := db.Put(ctx, "telegram_chat", telegramChatKey(42, 91), pending, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "sessions.reset", map[string]any{"id": "telegram_42"}); err == nil || !strings.Contains(err.Error(), "processing") {
		t.Fatalf("reset with pending update error=%v", err)
	}
	pending.Status = "failed"
	if err := db.Put(ctx, "telegram_chat", telegramChatKey(42, 91), pending, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "sessions.reset", map[string]any{"id": "telegram_42"}); err != nil {
		t.Fatalf("reset after update settled: %v", err)
	}
}

func TestSessionResetInvalidatesOrdinaryTelegramUpdateReplay(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.TelegramSession(ctx, 42, 7); err != nil {
		t.Fatal(err)
	}
	called := 0
	text, err := s.TelegramMessageOnce(ctx, 42, 100, 5, 7, "telegram_42", func() (string, error) { called++; return "old answer", nil })
	if err != nil || text != "old answer" {
		t.Fatalf("initial update text=%q err=%v", text, err)
	}
	if _, err := s.Action(ctx, "sessions.reset", map[string]any{"id": "telegram_42"}); err != nil {
		t.Fatal(err)
	}
	text, err = s.TelegramMessageOnce(ctx, 42, 100, 5, 7, "telegram_42_new", func() (string, error) { called++; return "new answer", nil })
	if err != nil || !strings.Contains(text, "已被重置") {
		t.Fatalf("replay after reset text=%q err=%v", text, err)
	}
	if called != 1 {
		t.Fatalf("generation calls=%d, want 1", called)
	}
	var record telegramChatRecord
	if err := db.Get(ctx, "telegram_chat", telegramChatKey(42, 100), &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != "invalidated" || record.Text != "" {
		t.Fatalf("reset left replay payload: %#v", record)
	}
}

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
