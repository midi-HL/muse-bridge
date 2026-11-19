package main

import (
	"context"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
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

func TestTelegramEnableAndWhitelistSwitchChangesRuntimeConfiguration(t *testing.T) {
	for _, setting := range []string{"telegram.enabled", "telegram.whitelist_enabled"} {
		t.Run(setting, func(t *testing.T) {
			values := map[string]any{"telegram.token": "synthetic", "telegram.mode": "polling", "telegram.enabled": true, "telegram.whitelist_enabled": false}
			r := newTelegramRuntime(context.Background(), nil)
			before := readTelegramConfig(values)
			r.token, r.mode = before.token, before.mode
			values[setting] = !values[setting].(bool)
			if r.sameConfig(readTelegramConfig(values)) {
				t.Fatal("Telegram runtime ignored " + setting)
			}
		})
	}
}

func TestDisabledTelegramStopsChannelDespiteSavedToken(t *testing.T) {
	ch, err := telegram.New(map[string]any{"telegram.token": "synthetic"}, telegram.Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newTelegramRuntime(ctx, nil)
	r.ch, r.token, r.mode = ch, "synthetic", "polling"
	if err := r.apply(map[string]any{"telegram.enabled": false, "telegram.token": "synthetic", "telegram.mode": "polling"}); err != nil {
		t.Fatal(err)
	}
	defer r.close()
	if r.ch != nil {
		t.Fatal("disabled Telegram kept an active channel")
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
