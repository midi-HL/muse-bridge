package main

import (
	"context"
	"testing"
)

func TestTelegramConfigIgnoresUnrelatedSettings(t *testing.T) {
	r := newTelegramRuntime(context.Background(), nil)
	cfg := readTelegramConfig(map[string]any{
		"telegram.token":     "123:secret",
		"telegram.mode":      "polling",
		"telegram.whitelist": "1,2",
	})
	r.token, r.mode, r.whitelist = cfg.token, cfg.mode, cfg.whitelist
	if !r.sameConfig(cfg) {
		t.Fatal("identical Telegram config was not recognized")
	}
	changed := readTelegramConfig(map[string]any{
		"telegram.token":     "123:secret",
		"telegram.mode":      "polling",
		"telegram.whitelist": "1,2,3",
	})
	if r.sameConfig(changed) {
		t.Fatal("Telegram whitelist change was ignored")
	}
}
