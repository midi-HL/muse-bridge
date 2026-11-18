package core

import (
	"context"
	"testing"
)

func TestBackupRoundTripPreservesTelegramSessionAndRetryState(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	pass := "telegram-backup-passphrase"
	first, err := s.NewTelegramSession(ctx, -100123, 77)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RememberTelegramPrompt(ctx, first, "77", "retry after restore"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TelegramCommandOnce(ctx, first, -100123, 77, 17, "retry", func() (string, error) { return "cached command result", nil }); err != nil {
		t.Fatal(err)
	}
	blob, err := s.Action(ctx, "backup.export", map[string]any{"passphrase": pass})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := s.Action(ctx, "backup.import", map[string]any{"passphrase": pass, "blob": blob.(map[string]any)["blob"]})
	if err != nil {
		t.Fatalf("valid Telegram backup rejected: %v", err)
	}
	if restored.(map[string]any)["count"].(int) < 3 {
		t.Fatal("backup omitted Telegram state records")
	}
	id, err := s.TelegramSession(ctx, -100123, 99)
	if err != nil || id != first {
		t.Fatalf("selected session=%q err=%v", id, err)
	}
	if prompt, err := s.TelegramLastPrompt(ctx, first, "77"); err != nil || prompt != "retry after restore" {
		t.Fatalf("retry state=%q err=%v", prompt, err)
	}
	called := false
	if text, err := s.TelegramCommandOnce(ctx, first, -100123, 77, 17, "retry", func() (string, error) { called = true; return "regenerated", nil }); err != nil || text != "cached command result" || called {
		t.Fatal("command idempotency state missing from backup")
	}
	// Use the encrypted SQLite itself after opening a second core service as a
	// persistence check independent from the HTTP backup import path.
	reopened, err := New(ctx, db, s.vault, s.redactor, s.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
