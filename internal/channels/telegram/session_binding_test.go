package telegram

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type bindingCallbacks struct {
	Callbacks
	selected *string
	seen     chan string
}

func (b bindingCallbacks) BindSession(_ context.Context, r Request) (Request, error) {
	r.SessionID = *b.selected
	return r, nil
}
func TestAcceptedUpdateKeepsBoundSessionAcrossNewPointer(t *testing.T) {
	selected := "old-session"
	seen := make(chan string, 1)
	client, err := NewClient("123:secret", &http.Client{Transport: commandTargetRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":8}}`)), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	h := bindingCallbacks{selected: &selected, seen: seen}
	h.Callbacks = Callbacks{OnChat: func(_ context.Context, r Request, _ func(string) error) (Response, error) {
		seen <- r.SessionID
		return Response{Text: "ok"}, nil
	}}
	ch := &Channel{client: client, handler: h, whitelist: map[int64]bool{}}
	u := update{}
	u.Message.Chat.ID = 42
	u.Message.From.ID = 7
	u.Message.Text = "accepted before /new"
	bound, err := ch.bindUpdate(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	selected = "new-session"
	if err = ch.handleRequest(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != "old-session" {
		t.Fatalf("accepted update followed new pointer: %q", got)
	}
}
