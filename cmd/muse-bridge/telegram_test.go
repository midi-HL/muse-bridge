package main

import (
	"context"
	"testing"
)

func TestTelegramConfigIgnoresUnrelatedSettings(t *testing.T) {
	r := newTelegramRuntime(context.Background(), nil)
	cfg := readTelegramConfig(map[string]any{"telegram.token": "123:secret", "telegram.mode": "polling", "telegram.whitelist": "1,2"})
	r.token, r.mode, r.whitelist = cfg.token, cfg.mode, cfg.whitelist
	if !r.sameConfig(cfg) {
		t.Fatal("identical Telegram config was not recognized")
	}
	changed := readTelegramConfig(map[string]any{"telegram.token": "123:secret", "telegram.mode": "polling", "telegram.whitelist": "1,2,3"})
	if r.sameConfig(changed) {
		t.Fatal("Telegram whitelist change was ignored")
	}
}

func TestTelegramBotUsernameChangesRuntimeConfiguration(t *testing.T) {
	before := readTelegramConfig(map[string]any{"telegram.token": "token", "telegram.mode": "polling", "telegram.bot_username": "first_bot"})
	runtime := &telegramRuntime{token: before.token, mode: before.mode, secret: before.secret, webhookURL: before.webhookURL, whitelist: before.whitelist, botUsername: before.botUsername}
	after := readTelegramConfig(map[string]any{"telegram.token": "token", "telegram.mode": "polling", "telegram.bot_username": "second_bot"})
	if runtime.sameConfig(after) {
		t.Fatal("bot username change was ignored")
	}
}
