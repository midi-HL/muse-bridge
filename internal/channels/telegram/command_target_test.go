package telegram

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type commandTargetRoundTrip func(*http.Request) (*http.Response, error)

func (f commandTargetRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCommandsTargetedAtOtherBotsAreIgnored(t *testing.T) {
	var commands, sends atomic.Int32
	client, err := NewClient("123:secret", &http.Client{Transport: commandTargetRoundTrip(func(*http.Request) (*http.Response, error) {
		sends.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	handler := Callbacks{OnCommand: func(_ context.Context, _ Request, c Command) (Response, error) {
		commands.Add(1)
		if c.Target != "mybot" {
			t.Fatalf("Target=%q", c.Target)
		}
		return Response{Text: "ok"}, nil
	}}
	ch := &Channel{client: client, handler: handler, botUsername: "mybot", whitelist: map[int64]bool{}}
	mk := func(text string) update {
		var u update
		u.Message.Chat.ID = 42
		u.Message.Chat.Type = "group"
		u.Message.From.ID = 7
		u.Message.MessageID = 1
		u.Message.Text = text
		return u
	}
	if err := ch.HandleUpdate(context.Background(), mk("/stop@other_bot")); err != nil {
		t.Fatal(err)
	}
	if commands.Load() != 0 || sends.Load() != 0 {
		t.Fatal("command directed at another bot had side effects")
	}
	if err := ch.HandleUpdate(context.Background(), mk("/stop@mybot")); err != nil {
		t.Fatal(err)
	}
	if commands.Load() != 1 || sends.Load() != 1 {
		t.Fatal("matching bot-targeted command was not delivered")
	}
}
func TestUntargetedCommandsRemainAvailableWithoutBotUsername(t *testing.T) {
	var calls atomic.Int32
	ch := &Channel{client: &Client{http: &http.Client{Transport: commandTargetRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Header: make(http.Header)}, nil
	})}}, handler: Callbacks{OnCommand: func(context.Context, Request, Command) (Response, error) { calls.Add(1); return Response{}, nil }}, whitelist: map[int64]bool{}}
	u := update{}
	u.Message.Chat.ID = 4
	u.Message.From.ID = 5
	u.Message.Text = "/help"
	if err := ch.HandleUpdate(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("untargeted command was rejected")
	}
}
