package core

import (
	"context"
	"github.com/midi-HL/muse-bridge/internal/pool"
	"testing"
)

func TestDuplicateCookieImportPreservesRoutingMetadata(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	if err := s.db.Put(ctx, "node", "proxy-node", map[string]any{"id": "proxy-node", "enabled": true}, map[string]string{"url": "http://127.0.0.1:18080"}); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.Add(ctx, pool.Account{ID: "legacy-id", Label: "Original", Enabled: false, Status: "disabled", NodeID: "proxy-node", Pool: "api", Note: "retain me"}, map[string]string{"hatch_sess": "old-cookie"}); err != nil {
		t.Fatal(err)
	}
	text := `{"id":"legacy-id","label":"Refreshed","cookies":{"hatch_sess":"new-cookie"}}`
	if _, err := s.Action(ctx, "accounts.import", map[string]any{"text": text}); err != nil {
		t.Fatal(err)
	}
	got := s.pool.List()[0]
	if got.NodeID != "proxy-node" || got.Pool != "api" || got.Note != "retain me" || got.Enabled || got.Status != "disabled" {
		t.Fatalf("routing metadata lost on cookie refresh: %#v", got)
	}
	var secret map[string]string
	if err := s.db.Secret(ctx, "account", "legacy-id", &secret); err != nil || secret["hatch_sess"] != "new-cookie" {
		t.Fatalf("cookie was not refreshed: %#v %v", secret, err)
	}
}
