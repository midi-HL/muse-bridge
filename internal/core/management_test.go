package core

import (
	"context"
	"strings"
	"testing"
)

func TestManagementActionsPersistAndMask(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.Action(ctx, "nodes.save", map[string]any{
		"id": "proxy-1", "name": "Local proxy", "url": "socks5://user:password@127.0.0.1:1080", "enabled": true,
	}); err != nil {
		t.Fatal(err)
	}
	state, err := s.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nodes, ok := state["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("unexpected nodes state: %#v", state["nodes"])
	}
	node := nodes[0].(nodeRecord)
	if !strings.Contains(node.MaskedURL, "••••••••") || strings.Contains(node.MaskedURL, "password") {
		t.Fatalf("proxy credentials leaked: %#v", node)
	}
	var secret map[string]string
	if err := db.Secret(ctx, "node", "proxy-1", &secret); err != nil || secret["url"] != "socks5://user:password@127.0.0.1:1080" {
		t.Fatalf("proxy secret was not persisted privately: %#v %v", secret, err)
	}
	if _, err := s.Action(ctx, "personas.save", map[string]any{"id": "calm", "name": "Calm", "prompt": "Answer concisely."}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "schedules.save", map[string]any{"id": "daily", "name": "Daily", "platform": "web", "target": "session-1", "prompt": "Summarize.", "cron": "0 9 * * *", "timezone": "Asia/Kuala_Lumpur"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "schedules.save", map[string]any{"id": "bad", "name": "Bad", "platform": "web", "target": "s", "prompt": "p", "cron": "every day", "timezone": "Asia/Kuala_Lumpur"}); err == nil {
		t.Fatal("invalid cron was accepted")
	}
}

func TestStickerImportRequiresConfiguredTokenAndValidURL(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	_, err := s.Action(ctx, "stickers.import", map[string]any{"url": "https://t.me/addstickers/demo"})
	if !errors.Is(err, telegram.ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"values": map[string]any{"telegram.token": "123:secret"}}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Action(ctx, "stickers.import", map[string]any{"url": "https://evil.example/addstickers/demo"})
	if err == nil || !strings.Contains(err.Error(), "sticker set URL is invalid") {
		t.Fatalf("expected URL validation error, got %v", err)
	}
}

func TestBackupImportOverwritesExistingRecords(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	pass := "backup-passphrase-2026"
	if _, err := s.Action(ctx, "personas.save", map[string]any{"id": "persona-1", "name": "Original", "prompt": "Original prompt"}); err != nil {
		t.Fatal(err)
	}
	exported, err := s.Action(ctx, "backup.export", map[string]any{"passphrase": pass})
	if err != nil {
		t.Fatal(err)
	}
	blob := exported.(map[string]any)["blob"].(string)
	if _, err := s.Action(ctx, "personas.save", map[string]any{"id": "persona-1", "name": "Changed", "prompt": "Changed prompt"}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Action(ctx, "backup.import", map[string]any{"passphrase": pass, "blob": blob})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["count"].(int) == 0 {
		t.Fatal("backup import wrote no records")
	}
	var item personaRecord
	if err := s.db.Get(ctx, "persona", "persona-1", &item); err != nil {
		t.Fatal(err)
	}
	if item.Name != "Original" || item.Prompt != "Original prompt" {
		t.Fatalf("backup did not overwrite record: %#v", item)
	}
}
