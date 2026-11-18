package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTelegramWebhookGuardRequiresCurrentSecret(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	secret := "first-secret"
	guard := telegramWebhookGuard{next: next, secret: func() string { return secret }}
	for _, value := range []string{"", "wrong"} {
		r := httptest.NewRequest(http.MethodPost, "http://example.test/telegram/webhook", nil)
		if value != "" {
			r.Header.Set("X-Telegram-Bot-Api-Secret-Token", value)
		}
		w := httptest.NewRecorder()
		guard.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("secret %q accepted with %d", value, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "http://example.test/telegram/webhook", nil)
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	w := httptest.NewRecorder()
	guard.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("valid secret rejected: %d", w.Code)
	}
	secret = "next-secret"
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "first-secret")
	w = httptest.NewRecorder()
	guard.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("old secret accepted after rotation: %d", w.Code)
	}
}
