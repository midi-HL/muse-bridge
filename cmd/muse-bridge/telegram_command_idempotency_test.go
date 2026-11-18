package main

import (
	"context"
	"encoding/base64"
	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
	"testing"
)

func TestTelegramCommandDuplicateUpdateDoesNotRunNewTwice(t *testing.T) {
	ctx := context.Background()
	v, err := secure.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir(), v)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := core.New(ctx, db, v, secure.NewRedactor(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	h := &telegramServiceHandler{service: svc}
	req := telegram.Request{ChatID: 123, UserID: 7, MessageID: 55}
	first, err := h.Command(ctx, req, telegram.Command{Name: "new"})
	if err != nil || first.Text == "" {
		t.Fatal(err)
	}
	var pointer map[string]any
	if err = db.Get(ctx, "telegram_session", "chat_123", &pointer); err != nil {
		t.Fatal(err)
	}
	sessionA := pointer["session_id"]
	second, err := h.Command(ctx, req, telegram.Command{Name: "new"})
	if err != nil || second.Text != first.Text {
		t.Fatal(err)
	}
	if err = db.Get(ctx, "telegram_session", "chat_123", &pointer); err != nil {
		t.Fatal(err)
	}
	if pointer["session_id"] != sessionA {
		t.Fatal("duplicate webhook update ran /new twice")
	}
}
