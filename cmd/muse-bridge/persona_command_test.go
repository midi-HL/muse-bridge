package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func TestTelegramPersonaCommandPersistsOwnedSelection(t *testing.T) {
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
	if _, err = svc.Action(ctx, "personas.save", map[string]any{"id": "calm", "name": "Calm", "prompt": "Answer calmly."}); err != nil {
		t.Fatal(err)
	}
	handler := telegramServiceHandler{service: svc}
	user := telegram.Request{ChatID: 42, UserID: 42}
	list, err := handler.Command(ctx, user, telegram.Command{Name: "persona"})
	if err != nil || !strings.Contains(list.Text, "calm") {
		t.Fatal("persona command did not list saved preset")
	}
	if _, err = handler.Command(ctx, user, telegram.Command{Name: "persona", Args: "calm"}); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = db.Get(ctx, "session", "telegram_42", &record); err != nil {
		t.Fatal(err)
	}
	if record["persona_id"] != "calm" || record["user_id"] != "42" {
		t.Fatal("command did not persist owned persona selection")
	}
	before, _ := json.Marshal(record)
	if _, err = handler.Command(ctx, telegram.Request{ChatID: 42, UserID: 99}, telegram.Command{Name: "persona", Args: "none"}); err != nil {
		t.Fatal(err)
	}
	if err = db.Get(ctx, "session", "telegram_42", &record); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(record)
	if string(before) != string(after) {
		t.Fatal("foreign sender changed persona")
	}
	if _, err = handler.Command(ctx, user, telegram.Command{Name: "persona", Args: "none"}); err != nil {
		t.Fatal(err)
	}
	if err = db.Get(ctx, "session", "telegram_42", &record); err != nil {
		t.Fatal(err)
	}
	if record["persona_id"] != "" || record["persona_selected"] != true {
		t.Fatal("none selection was not pinned")
	}
}
