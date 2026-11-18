package core

import (
	"context"
	"errors"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func TestTelegramRetryPromptsArePerSender(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	if err := s.RememberTelegramPrompt(ctx, "shared-session", "sender-1", "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberTelegramPrompt(ctx, "shared-session", "sender-2", "two"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TelegramLastPrompt(ctx, "shared-session", "sender-1"); err != nil || got != "one" {
		t.Fatalf("sender1=%q %v", got, err)
	}
	if got, err := s.TelegramLastPrompt(ctx, "shared-session", "sender-2"); err != nil || got != "two" {
		t.Fatalf("sender2=%q %v", got, err)
	}
}

func TestRetryReusesFailedAttemptWithoutDuplicatingUserHistory(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.TelegramSession(ctx, 42, 12); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "accounts.import", map[string]any{"id": "retry", "cookies": map[string]string{"hatch_sess": "retry-cookie"}}); err != nil {
		t.Fatal(err)
	}
	s.generate = func(context.Context, *hatch.Client, hatch.GenerateRequest, func(hatch.Event) error) (hatch.GenerateResult, error) {
		return hatch.GenerateResult{}, errors.New("first failure")
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "telegram_42", Platform: "telegram", UserID: "42", RetryUserID: "12", Prompt: "first request"}, nil); err == nil {
		t.Fatal("expected first failure")
	}
	var before sessionRecord
	if err := db.Get(ctx, "session", "telegram_42", &before); err == nil && len(before.Messages) != 0 {
		t.Fatalf("failed generation polluted history: %#v", before.Messages)
	}
	attemptID, err := s.TelegramRetryAttempt(ctx, "telegram_42", "12")
	if err != nil {
		t.Fatal(err)
	}
	s.generate = func(_ context.Context, _ *hatch.Client, req hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
		if req.Prompt != "first request" {
			t.Fatalf("retry prompt=%q", req.Prompt)
		}
		return hatch.GenerateResult{Text: "success"}, nil
	}
	if _, err = s.Chat(ctx, ChatRequest{SessionID: "telegram_42", Platform: "telegram", UserID: "42", RetryUserID: "12", RetryAttemptID: attemptID, Retry: true}, nil); err != nil {
		t.Fatal(err)
	}
	var after sessionRecord
	if err = db.Get(ctx, "session", "telegram_42", &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Messages) != 2 || after.Messages[0].Role != "user" || after.Messages[1].Role != "assistant" {
		t.Fatalf("retry turn=%#v", after.Messages)
	}
	s.generate = func(context.Context, *hatch.Client, hatch.GenerateRequest, func(hatch.Event) error) (hatch.GenerateResult, error) {
		return hatch.GenerateResult{}, errors.New("second failure")
	}
	if _, err = s.Chat(ctx, ChatRequest{SessionID: "telegram_42", Platform: "telegram", UserID: "42", RetryUserID: "12", RetryAttemptID: attemptID, Retry: true}, nil); err == nil {
		t.Fatal("expected retry failure")
	}
	var failed sessionRecord
	if err = db.Get(ctx, "session", "telegram_42", &failed); err != nil {
		t.Fatal(err)
	}
	if len(failed.Messages) != len(after.Messages) {
		t.Fatal("failed retry polluted history")
	}
}
