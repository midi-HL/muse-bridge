package core

import (
	"context"
	"testing"
)

func TestSettingsUpdateEmptySecretClearsEncryptedValue(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.Action(ctx, "settings.update", map[string]any{"values": map[string]any{"telegram.token": "secret-token"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"values": map[string]any{"telegram.token": ""}}); err != nil {
		t.Fatal(err)
	}
	var secrets map[string]string
	if err := db.Secret(ctx, "settings", "current", &secrets); err == nil && secrets["telegram.token"] != "" {
		t.Fatalf("secret was not cleared: %#v", secrets)
	}
	if got := s.RuntimeSettings()["telegram.token"]; got != "" {
		t.Fatalf("runtime secret was not cleared: %#v", got)
	}
}
