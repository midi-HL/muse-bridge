package main

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func TestTelegramPersonaTargetsSharedChatSessionAndChecksSender(t *testing.T) {
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
	session, err := h.currentSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	list, err := h.Command(ctx, telegram.Request{ChatID: req.ChatID, UserID: 99}, telegram.Command{Name: "persona"})
	if err != nil || !strings.Contains(list.Text, "calm") {
		t.Fatal("chat member could not list persona presets")
	}
	if _, err = h.Command(ctx, req, telegram.Command{Name: "persona", Args: "calm"}); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = db.Get(ctx, "session", session, &record); err != nil {
		t.Fatal(err)
	}
	if record["persona_id"] != "calm" || record["user_id"] != "-10042" {
		t.Fatalf("persona not applied to shared chat session: %#v", record)
	}
	if _, err = svc.SetSessionPersona(ctx, session, "telegram", "8", "_none"); err == nil {
		t.Fatal("another sender switched shared chat persona")
	}
}
