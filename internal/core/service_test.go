package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func testService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	vault, err := secure.NewVault(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir(), vault)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), db, vault, secure.NewRedactor(), t.TempDir(), map[string]any{"vision.key": "secret-value"})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); _ = db.Close() })
	return s, db
}

func TestSettingsMaskAndEncryptedPersistence(t *testing.T) {
	s, db := testService(t)
	settings := s.Settings()
	masked, ok := settings["vision.key"].(map[string]any)
	if !ok || masked["configured"] != true || masked["masked"] == "secret-value" {
		t.Fatalf("secret was not masked: %#v", settings["vision.key"])
	}
	var public map[string]any
	if err := db.Get(context.Background(), "settings", "current", &public); err != nil {
		t.Fatal(err)
	}
	if _, ok := public["vision.key"]; ok {
		t.Fatal("secret leaked into public settings record")
	}
	var secret map[string]string
	if err := db.Secret(context.Background(), "settings", "current", &secret); err != nil {
		t.Fatal(err)
	}
	if secret["vision.key"] != "secret-value" {
		t.Fatalf("secret was not persisted encrypted: %#v", secret)
	}
}

func TestActionAliasesAndUnsupported(t *testing.T) {
	s, _ := testService(t)
	if _, err := s.Action(context.Background(), "accounts.list", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(context.Background(), "unknown.operation", nil); err == nil || !errors.Is(err, errors.New("unsupported action: unknown.operation")) && err.Error() != "unsupported action: unknown.operation" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionIsolationRejectsDifferentUser(t *testing.T) {
	s, db := testService(t)
	item := sessionRecord{ID: "session-a", Platform: "api", UserID: "user-a"}
	if err := db.Put(context.Background(), "session", item.ID, item, nil); err != nil {
		t.Fatal(err)
	}
	result, err := s.Chat(context.Background(), ChatRequest{SessionID: item.ID, Platform: "api", UserID: "user-b", Prompt: "hello"}, nil)
	if err == nil || result.SessionID != item.ID {
		t.Fatalf("expected isolated session rejection, result=%#v err=%v", result, err)
	}
}
