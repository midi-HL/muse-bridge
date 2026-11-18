package core

import (
	"context"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"testing"
)

func TestRetryWithSameTextAsAnotherUsersTurnDoesNotReplaceIt(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.TelegramSession(ctx, 42, 7); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(ctx, "session", "telegram_42", sessionRecord{ID: "telegram_42", Platform: "telegram", UserID: "42", Messages: []hatch.Message{{Role: "user", Content: "same"}, {Role: "assistant", Content: "other sender success"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberTelegramPrompt(ctx, "telegram_42", "7", "same"); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.TelegramRetryAttempt(ctx, "telegram_42", "7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Action(ctx, "accounts.import", map[string]any{"id": "chatretry", "cookies": map[string]string{"hatch_sess": "synthetic"}}); err != nil {
		t.Fatal(err)
	}
	s.generate = func(context.Context, *hatch.Client, hatch.GenerateRequest, func(hatch.Event) error) (hatch.GenerateResult, error) {
		return hatch.GenerateResult{Text: "sender seven retry"}, nil
	}
	if _, err = s.Chat(ctx, ChatRequest{SessionID: "telegram_42", Platform: "telegram", UserID: "42", RetryUserID: "7", RetryAttemptID: attempt, Retry: true}, nil); err != nil {
		t.Fatal(err)
	}
	var after sessionRecord
	if err = db.Get(ctx, "session", "telegram_42", &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Messages) != 4 || after.Messages[1].Content != "other sender success" || after.Messages[2].Role != "user" || after.Messages[3].Content != "sender seven retry" {
		t.Fatalf("retry replaced another sender's successful turn: %#v", after.Messages)
	}
}
