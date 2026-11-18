package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func TestTelegramGroupWebhookPreservesSpeakersAndRetryInput(t *testing.T) {
	ctx := context.Background()
	svc, db := newTelegramIntegrationService(t)
	h := &telegramServiceHandler{service: svc}
	var prompts []string
	var modelMessages [][]hatch.Message
	svc.SetGenerateForTest(func(_ context.Context, _ *hatch.Client, req hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
		prompts = append(prompts, req.Prompt)
		modelMessages = append(modelMessages, append([]hatch.Message(nil), req.Messages...))
		if len(prompts) == 2 {
			return hatch.GenerateResult{}, errors.New("synthetic failure for second speaker")
		}
		return hatch.GenerateResult{Text: "answer"}, nil
	})

	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	var lookups, sends int
	http.DefaultTransport = telegramTestTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/getMe"):
			lookups++
			body = `{"ok":true,"result":{"id":123,"is_bot":true,"username":"my_bot"}}`
		case strings.HasSuffix(req.URL.Path, "/sendMessage"):
			sends++
			body = `{"ok":true,"result":{"message_id":100}}`
		case strings.HasSuffix(req.URL.Path, "/editMessageText"), strings.HasSuffix(req.URL.Path, "/deleteMessage"):
			body = `{"ok":true,"result":true}`
		default:
			return nil, errors.New("unexpected Bot API method")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	ch, err := telegram.New(map[string]any{"telegram.token": "123:synthetic", "telegram.mode": "webhook", "telegram.whitelist": "7,8"}, h)
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(payload string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		ch.WebhookHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(payload)))
		if w.Code != want {
			t.Fatalf("Webhook status=%d, want %d", w.Code, want)
		}
	}
	const ignored = `{"update_id":1,"message":{"message_id":1,"chat":{"id":-42,"type":"group"},"from":{"id":7},"text":"ambient conversation"}}`
	const denied = `{"update_id":2,"message":{"message_id":2,"chat":{"id":-42,"type":"group"},"from":{"id":9},"text":"@my_bot blocked","entities":[{"type":"mention","offset":0,"length":7}]}}`
	deliver(ignored, http.StatusNoContent)
	deliver(denied, http.StatusNoContent)
	var pointer map[string]any
	if err := db.Get(ctx, "telegram_session", "chat_-42", &pointer); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ignored updates should leave the session pointer absent: %v", err)
	}
	if len(prompts) != 0 || lookups != 0 || sends != 0 {
		t.Fatalf("ignored updates caused model or Bot API activity: prompts=%d lookups=%d sends=%d", len(prompts), lookups, sends)
	}

	const first = `{"update_id":3,"message":{"message_id":3,"chat":{"id":-42,"type":"group"},"from":{"id":7,"first_name":"Alice","username":"alice"},"text":"@my_bot my first message","entities":[{"type":"mention","offset":0,"length":7}]}}`
	const second = `{"update_id":4,"message":{"message_id":4,"chat":{"id":-42,"type":"group"},"from":{"id":8,"first_name":"Bob"},"text":"and my reply","reply_to_message":{"from":{"id":123,"is_bot":true}}}}`
	const retry = `{"update_id":5,"message":{"message_id":5,"chat":{"id":-42,"type":"group"},"from":{"id":8,"first_name":"Renamed Bob","username":"bob_new"},"text":"/retry"}}`
	deliver(first, http.StatusNoContent)
	deliver(second, http.StatusInternalServerError)
	var history sessionRecordForTest
	if err := db.Get(ctx, "session", "telegram_-42", &history); err != nil || len(history.Messages) != 2 {
		t.Fatalf("history after failed generation: %#v, %v", history.Messages, err)
	}
	deliver(retry, http.StatusNoContent)
	const alice = "[Alice (@alice)]: @my_bot my first message"
	const bob = "[Bob]: and my reply"
	if !reflect.DeepEqual(prompts, []string{alice, bob, bob}) || lookups != 1 {
		t.Fatalf("model prompts=%q, getMe=%d", prompts, lookups)
	}
	want := []hatch.Message{{Role: "user", Content: alice}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: bob}}
	if !reflect.DeepEqual(modelMessages[2], want) {
		t.Fatalf("retry transcript=%#v", modelMessages[2])
	}
	if err := db.Get(ctx, "session", "telegram_-42", &history); err != nil {
		t.Fatal(err)
	}
	want = append(want, hatch.Message{Role: "assistant", Content: "answer"})
	if !reflect.DeepEqual(history.Messages, want) {
		t.Fatalf("stored transcript=%#v", history.Messages)
	}
}
