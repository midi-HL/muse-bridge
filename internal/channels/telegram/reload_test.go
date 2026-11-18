package telegram

import (
	"context"
	"testing"
)

func TestReloadUpdatesWhitelistAndRejectsMissingToken(t *testing.T) {
	ch, err := New(map[string]any{"telegram.token": "123:secret", "telegram.mode": "polling", "telegram.whitelist": "1,2"}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Reload(context.Background(), map[string]any{"telegram.token": "456:secret", "telegram.mode": "polling", "telegram.whitelist": "9"}); err != nil {
		t.Fatal(err)
	}
	ch.mu.Lock()
	valid := ch.client != nil && ch.client.token == "456:secret" && ch.whitelist[9] && !ch.whitelist[1]
	ch.mu.Unlock()
	if !valid {
		t.Fatalf("reload not applied")
	}
	if err := ch.Reload(context.Background(), map[string]any{"telegram.token": ""}); err != ErrNotConfigured {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}
