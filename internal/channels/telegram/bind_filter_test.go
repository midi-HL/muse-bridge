package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type bindFilterHandler struct{ bound atomic.Int32 }

func (h *bindFilterHandler) BindSession(_ context.Context, r Request) (Request, error) {
	h.bound.Add(1)
	r.SessionID = "created-by-bind"
	return r, nil
}
func (h *bindFilterHandler) Chat(context.Context, Request, func(string) error) (Response, error) {
	return Response{}, nil
}
func (h *bindFilterHandler) Command(context.Context, Request, Command) (Response, error) {
	return Response{}, nil
}
func (h *bindFilterHandler) Sticker(context.Context, string) (StickerAsset, error) {
	return StickerAsset{}, ErrUnsupported
}
func (h *bindFilterHandler) Feedback(context.Context, Request, Delivery) error { return nil }

func TestWebhookRejectedUpdateDoesNotBindSession(t *testing.T) {
	h := &bindFilterHandler{}
	client, err := NewClient("123:secret", &http.Client{Transport: commandTargetRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ch := &Channel{client: client, handler: h, botUsername: "mybot", whitelist: map[int64]bool{7: false}}
	deliver := func(text string) {
		t.Helper()
		payload := `{"message":{"message_id":1,"from":{"id":7},"chat":{"id":42,"type":"group"},"text":"` + text + `"}}`
		req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(payload))
		w := httptest.NewRecorder()
		ch.WebhookHandler().ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("webhook status=%d", w.Code)
		}
	}
	deliver("/new@mybot")
	if h.bound.Load() != 0 {
		t.Fatal("non-whitelisted update bound a session")
	}
	ch.whitelist = map[int64]bool{7: true}
	deliver("/new@other_bot")
	if h.bound.Load() != 0 {
		t.Fatal("foreign-target command bound a session")
	}
	deliver("/new@mybot")
	if h.bound.Load() != 1 {
		t.Fatal("valid command failed to bind exactly once")
	}
}
