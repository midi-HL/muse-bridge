package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func groupTextUpdate(id int, chatType, text, metadata string) string {
	encoded, _ := json.Marshal(text)
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":-42,"type":%q},"from":{"id":7},"text":%s%s}}`, id, id, chatType, encoded, metadata)
}

func TestWebhookGroupTriggerBeforeSessionBinding(t *testing.T) {
	cases := []struct {
		name, chatType, text, metadata, username string
		allowed, lookup                          bool
	}{
		{name: "ordinary", chatType: "group", text: "hello @my_bot", username: "my_bot"},
		{name: "lookalike", chatType: "supergroup", text: "@my_bot_other hello", metadata: `,"entities":[{"type":"mention","offset":0,"length":13}]`, username: "my_bot", lookup: true},
		{name: "other bot reply", chatType: "group", text: "hello", metadata: `,"reply_to_message":{"from":{"id":456,"is_bot":true,"username":"my_bot"}}`, username: "my_bot", lookup: true},
		{name: "own bot reply", chatType: "group", text: "hello", metadata: `,"reply_to_message":{"from":{"id":123,"is_bot":true}}`, allowed: true, lookup: true},
		{name: "UTF-16 mention", chatType: "supergroup", text: "😀 @My_Bot hello", metadata: `,"entities":[{"type":"mention","offset":3,"length":7}]`, username: "my_bot", allowed: true, lookup: true},
		{name: "configured name is not the bot", chatType: "group", text: "@old_bot hello", metadata: `,"entities":[{"type":"mention","offset":0,"length":8}]`, username: "old_bot", lookup: true},
		{name: "malformed entity", chatType: "group", text: "@my_bot hello", metadata: `,"entities":[{"type":"mention","offset":-1,"length":7}]`, username: "my_bot"},
		{name: "discover username", chatType: "group", text: "@my_bot hello", metadata: `,"entities":[{"type":"mention","offset":0,"length":7}]`, allowed: true, lookup: true},
		{name: "group command", chatType: "group", text: "/stop", allowed: true},
		{name: "private", chatType: "private", text: "hello", allowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &bindFilterHandler{}
			var outbound, lookups int
			client, err := NewClient("123:synthetic", &http.Client{Transport: pollingTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/getMe") {
					lookups++
					return pollingResponse(map[string]any{"id": 123, "is_bot": true, "username": "my_bot"}), nil
				}
				outbound++
				return pollingResponse(map[string]any{"message_id": 1}), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			ch := &Channel{client: client, handler: h, botUsername: tc.username, whitelist: map[int64]bool{7: true}}
			for id := 1; id <= 2; id++ {
				w := httptest.NewRecorder()
				ch.WebhookHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(groupTextUpdate(id, tc.chatType, tc.text, tc.metadata))))
				if w.Code != http.StatusNoContent {
					t.Fatalf("status=%d", w.Code)
				}
			}
			if tc.allowed != (h.bound.Load() == 2) || (!tc.allowed && (h.bound.Load() != 0 || outbound != 0)) {
				t.Fatalf("allowed=%v, bound=%d, outbound=%d", tc.allowed, h.bound.Load(), outbound)
			}
			if tc.lookup != (lookups == 1) || (!tc.lookup && lookups != 0) {
				t.Fatalf("getMe calls=%d", lookups)
			}
		})
	}
}

func TestPollingGroupTriggerAndOffset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	h := &bindFilterHandler{}
	var offsets []string
	var lookups int
	ch := newPollingAckChannel(t, h, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			offset := r.Form.Get("offset")
			offsets = append(offsets, offset)
			if offset == "43" {
				cancel()
				return nil, r.Context().Err()
			}
			return pollingResponse([]json.RawMessage{
				json.RawMessage(groupTextUpdate(40, "supergroup", "ignore this", "")),
				json.RawMessage(groupTextUpdate(41, "group", "/help", "")),
				json.RawMessage(groupTextUpdate(42, "supergroup", "@my_bot hello", `,"entities":[{"type":"mention","offset":0,"length":7}]`)),
			}), nil
		}
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			lookups++
			return pollingResponse(map[string]any{"id": 123, "is_bot": true, "username": "my_bot"}), nil
		}
		return pollingResponse(map[string]any{"message_id": 1}), nil
	})
	if err := ch.Run(ctx); err != context.Canceled {
		t.Fatalf("Run returned %v", err)
	}
	if h.bound.Load() != 2 || lookups != 1 || len(offsets) < 2 || offsets[0] != "0" || offsets[len(offsets)-1] != "43" {
		t.Fatalf("bindings=%d, lookups=%d, offsets=%v", h.bound.Load(), lookups, offsets)
	}
}

func TestGroupTriggerFailsClosedWhenBotIdentityUnavailable(t *testing.T) {
	h := &bindFilterHandler{}
	var lookups, outbound int
	client, err := NewClient("123:synthetic", &http.Client{Transport: pollingTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			lookups++
			return pollingAckFailure(), nil
		}
		outbound++
		return pollingResponse(map[string]any{"message_id": 1}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	client.identity.bot = botIdentity{ID: 123, IsBot: true, Username: "my_bot"}
	client.identity.validUntil = time.Now().Add(-time.Minute)
	ch := &Channel{client: client, handler: h, botUsername: "my_bot"}
	const mention = `,"entities":[{"type":"mention","offset":0,"length":7}]`
	for id := 1; id <= 2; id++ {
		w := httptest.NewRecorder()
		ch.WebhookHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(groupTextUpdate(id, "supergroup", "@my_bot hello", mention))))
		if w.Code != http.StatusNoContent {
			t.Fatalf("webhook status=%d", w.Code)
		}
	}
	if lookups != 1 || outbound != 0 || h.bound.Load() != 0 {
		t.Fatalf("lookups=%d, outbound=%d, bindings=%d", lookups, outbound, h.bound.Load())
	}
}
