package core

import (
	"context"
	"testing"
)

func TestTelegramNewSessionSwitchPersistsAcrossRestart(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	before, err := s.TelegramSession(ctx, -10042, 7)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.NewTelegramSession(ctx, -10042, 7)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("new Telegram session did not change conversation")
	}
	reopened, err := New(ctx, db, s.vault, s.redactor, s.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.TelegramSession(ctx, -10042, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != after {
		t.Fatalf("session after restart=%q want %q", got, after)
	}
}

func TestTelegramRetryPromptPersistsAndIsOwnedByChat(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	id, err := s.TelegramSession(ctx, 42, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RememberTelegramPrompt(ctx, id, "chat:42", "last attempted prompt"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TelegramLastPrompt(ctx, id, "chat:42"); err != nil || got != "last attempted prompt" {
		t.Fatalf("retry prompt=%q err=%v", got, err)
	}
	reopened, err := New(ctx, db, s.vault, s.redactor, s.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.TelegramLastPrompt(ctx, id, "chat:42"); err != nil || got != "last attempted prompt" {
		t.Fatalf("retry prompt after restart=%q err=%v", got, err)
	}
	if _, err = reopened.TelegramLastPrompt(ctx, id, "chat:foreign"); err == nil {
		t.Fatal("foreign Telegram chat read retry prompt")
	}
}
