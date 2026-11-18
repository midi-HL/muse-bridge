package core

import (
	"context"
	"testing"
)

func TestRuntimeSettingsRestoresPersistedTelegramSecret(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.Action(ctx, "settings.update", map[string]any{"values": map[string]any{"telegram.token": "123:secret", "telegram.mode": "webhook", "telegram.webhook_secret": "hook-secret"}}); err != nil {
		t.Fatal(err)
	}
	raw := s.RuntimeSettings()
	if raw["telegram.token"] != "123:secret" || raw["telegram.webhook_secret"] != "hook-secret" {
		t.Fatal("runtime settings did not retain raw secrets")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(context.Background(), db, s.vault, s.redactor, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got := reopened.RuntimeSettings()
	if got["telegram.token"] != "123:secret" || got["telegram.webhook_secret"] != "hook-secret" || got["telegram.mode"] != "webhook" {
		t.Fatalf("persisted runtime settings not restored: %#v", got)
	}
}
