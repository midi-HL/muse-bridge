package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWhitelistSettingControlsAdmissionBeforeBinding(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		list      string
		wantBound int32
	}{
		{"enabled-empty-denies-all", true, "", 0},
		{"enabled-listed-allows", true, "7", 1},
		{"enabled-other-denies", true, "8", 0},
		{"disabled-ignores-saved-list", false, "8", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &bindFilterHandler{}
			ch, err := New(map[string]any{"telegram.token": "synthetic", "telegram.mode": "webhook", "telegram.whitelist_enabled": tc.enabled, "telegram.whitelist": tc.list}, h)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(`{"message":{"message_id":1,"from":{"id":7},"chat":{"id":42,"type":"private"},"text":"/status"}}`))
			ch.WebhookHandler().ServeHTTP(w, r)
			if w.Code != http.StatusNoContent || h.bound.Load() != tc.wantBound {
				t.Fatalf("status=%d, bound=%d; want 204, %d", w.Code, h.bound.Load(), tc.wantBound)
			}
		})
	}
}

func TestReloadAppliesWhitelistEnabledWithEmptyList(t *testing.T) {
	ch, err := New(map[string]any{"telegram.token": "synthetic", "telegram.whitelist_enabled": false}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	var u update
	u.Message.Chat.ID, u.Message.From.ID, u.Message.Text = 42, 7, "/status"
	if !ch.acceptsUpdate(context.Background(), u) {
		t.Fatal("disabled whitelist rejected input")
	}
	if err := ch.Reload(context.Background(), map[string]any{"telegram.token": "synthetic", "telegram.whitelist_enabled": true, "telegram.whitelist": ""}); err != nil {
		t.Fatal(err)
	}
	if ch.acceptsUpdate(context.Background(), u) {
		t.Fatal("enabled empty whitelist accepted input after reload")
	}
}
