package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/pool"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func TestTelegramNewChatAndRetryUsePersistedSession(t *testing.T) {
	ctx := context.Background()
	vault, err := secure.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir(), vault)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := core.New(ctx, db, vault, secure.NewRedactor(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	h := &telegramServiceHandler{service: svc}
	req := telegram.Request{ChatID: 99, UserID: 9, Text: "synthetic prompt"}
	old, err := h.currentSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Command(ctx, req, telegram.Command{Name: "new"}); err != nil {
		t.Fatal(err)
	}
	next, err := h.currentSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if old == next {
		t.Fatal("/new did not change persistent session")
	}
	// With no account, the actual chat call deterministically fails before any
	// network connection while still exercising handler -> core routing.
	if _, err = h.Chat(ctx, req, func(string) error { return nil }); err == nil {
		t.Fatal("chat unexpectedly succeeded with empty account pool")
	}
	if got, err := svc.TelegramLastPrompt(ctx, next, "9"); err != nil || got != req.Text {
		t.Fatalf("current chat prompt not recorded for retry: %q %v", got, err)
	}
	if _, err = h.Command(ctx, req, telegram.Command{Name: "retry"}); !errors.Is(err, pool.ErrNoAccount) {
		t.Fatalf("retry did not dispatch against current session: %v", err)
	}
	var record map[string]any
	if err = db.Get(ctx, "session", next, &record); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed chat/retry polluted session history: %v", err)
	}
	// A fresh adapter reconstructs its pointer from the persistent chat record.
	reopened, err := core.New(ctx, db, vault, secure.NewRedactor(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	h2 := &telegramServiceHandler{service: reopened}
	persisted, err := h2.currentSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != next {
		t.Fatal("/new selection was not restored after restart")
	}
}

func TestTelegramPersonaCommandTargetsCurrentChatSession(t *testing.T) {
	ctx := context.Background()
	vault, err := secure.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir(), vault)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := core.New(ctx, db, vault, secure.NewRedactor(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err = svc.Action(ctx, "personas.save", map[string]any{"id": "calm", "name": "Calm", "prompt": "Answer calmly."}); err != nil {
		t.Fatal(err)
	}
	h := &telegramServiceHandler{service: svc}
	req := telegram.Request{ChatID: -10042, UserID: 7}
	if _, err = h.Command(ctx, req, telegram.Command{Name: "new"}); err != nil {
		t.Fatal(err)
	}
	id, err := h.currentSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Command(ctx, req, telegram.Command{Name: "persona", Args: "calm"}); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = db.Get(ctx, "session", id, &record); err != nil {
		t.Fatal(err)
	}
	if record["persona_id"] != "calm" || record["user_id"] != "-10042" {
		t.Fatalf("persona not applied to current chat session: %#v", record)
	}
}
