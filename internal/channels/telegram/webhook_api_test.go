package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type webhookRoundTrip func(*http.Request) (*http.Response, error)

func (f webhookRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestSetWebhookRejectsNonHTTPS(t *testing.T) {
	client, err := NewClient("123:secret", &http.Client{Transport: webhookRoundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("transport should not run"); return nil, nil })})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetWebhook(context.Background(), "http://example.test/telegram/webhook", "secret"); err == nil {
		t.Fatal("HTTP webhook URL accepted")
	}
}

func TestSetWebhookSendsSecretWithoutLeakingItInErrors(t *testing.T) {
	secret := "webhook-secret"
	var seen bool
	transport := webhookRoundTrip(func(r *http.Request) (*http.Response, error) {
		seen = strings.Contains(r.URL.Path, "/setWebhook") && r.FormValue("secret_token") == secret
		return &http.Response{StatusCode: 200, Body: ioNopCloser{strings.NewReader(`{"ok":true,"result":true}`)}, Header: make(http.Header)}, nil
	})
	client, err := NewClient("123:secret", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetWebhook(context.Background(), "https://example.test/telegram/webhook", secret); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("webhook request did not carry expected secret")
	}
}

func TestWebhookHandlerRejectsMalformedUpdates(t *testing.T) {
	ch, err := New(map[string]any{"telegram.token": "123:secret"}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	ch.WebhookHandler().ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("unexpected webhook response: %d", w.Code)
	}
}
