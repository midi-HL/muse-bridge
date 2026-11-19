package telegram

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSenderChatCannotUseCompatibilityUserWhitelist(t *testing.T) {
	h := &bindFilterHandler{}
	client, err := NewClient("123:synthetic", &http.Client{Transport: pollingTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("rejected update must not call Bot API")
		return nil, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ch := &Channel{client: client, handler: h, whitelist: map[int64]bool{1087968824: true}}
	const payload = `{"message":{"message_id":1,"chat":{"id":-42,"type":"supergroup"},"from":{"id":1087968824,"is_bot":true},"sender_chat":{"id":-42},"text":"/new"}}`
	w := httptest.NewRecorder()
	ch.WebhookHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(payload)))
	if w.Code != http.StatusNoContent || h.bound.Load() != 0 {
		t.Fatalf("status=%d bound=%d", w.Code, h.bound.Load())
	}
}
