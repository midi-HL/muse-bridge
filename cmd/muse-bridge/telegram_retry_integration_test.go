package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func newTelegramIntegrationService(t *testing.T) (*core.Service, *store.Store) {
	t.Helper()
	ctx := context.Background()
	v, err := secure.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir(), v)
	if err != nil {
		t.Fatal(err)
	}
	s, err := core.New(ctx, db, v, secure.NewRedactor(), t.TempDir(), nil)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = s.Action(ctx, "accounts.import", map[string]any{"id": "synthetic", "cookies": map[string]string{"hatch_sess": "synthetic-only"}}); err != nil {
		s.Close()
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); db.Close() })
	return s, db
}

func TestTelegramHandlerRetryAfterFailureAndStopCurrentSession(t *testing.T) {
	ctx := context.Background()
	svc, db := newTelegramIntegrationService(t)
	h := &telegramServiceHandler{service: svc}
	req := telegram.Request{ChatID: 4242, UserID: 12, Text: "retry this prompt"}
	session, err := h.currentSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc.SetGenerateForTest(func(_ context.Context, _ *hatch.Client, request hatch.GenerateRequest, emit func(hatch.Event) error) (hatch.GenerateResult, error) {
		calls++
		if request.Prompt != "retry this prompt" {
			t.Fatalf("generated prompt=%q", request.Prompt)
		}
		if calls == 1 {
			return hatch.GenerateResult{}, errors.New("synthetic first failure")
		}
		if calls == 2 {
			return hatch.GenerateResult{Text: "retry succeeded"}, nil
		}
		return hatch.GenerateResult{}, errors.New("unexpected extra generation")
	})
	if _, err = h.Chat(ctx, req, func(string) error { return nil }); err == nil {
		t.Fatal("first generation unexpectedly succeeded")
	}
	if resp, err := h.Command(ctx, telegram.Request{ChatID: req.ChatID, UserID: req.UserID}, telegram.Command{Name: "retry"}); err != nil || resp.Text != "retry succeeded" {
		t.Fatalf("/retry result=%q err=%v", resp.Text, err)
	}
	var history sessionRecordForTest
	if err = db.Get(ctx, "session", session, &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 2 || history.Messages[0].Role != "user" || history.Messages[0].Content != req.Text || history.Messages[1].Role != "assistant" {
		t.Fatalf("retry history=%#v", history.Messages)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	svc.SetGenerateForTest(func(run context.Context, _ *hatch.Client, _ hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
		close(started)
		<-run.Done()
		return hatch.GenerateResult{}, run.Err()
	})
	req.Text = "long running request"
	go func() { _, chatErr := h.Chat(ctx, req, func(string) error { return nil }); done <- chatErr }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("generation never started")
	}
	if _, err := h.Command(ctx, req, telegram.Command{Name: "new"}); err != nil {
		t.Fatal(err)
	}
	if resp, err := h.Command(ctx, req, telegram.Command{Name: "stop"}); err != nil || resp.Text == "" {
		t.Fatalf("/stop response=%q err=%v", resp.Text, err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stopped generation succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/stop did not cancel current chat session")
	}
}

type telegramTestTransport func(*http.Request) (*http.Response, error)

func (f telegramTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramWebhookRetryRedeliveryReusesReply(t *testing.T) {
	ctx := context.Background()
	svc, db := newTelegramIntegrationService(t)
	h := &telegramServiceHandler{service: svc}
	req := telegram.Request{ChatID: 4242, UserID: 12, Text: "retry this prompt"}
	session, err := h.currentSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var generations int
	svc.SetGenerateForTest(func(_ context.Context, _ *hatch.Client, r hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
		generations++
		if r.Prompt != req.Text {
			t.Fatalf("generated prompt=%q", r.Prompt)
		}
		if generations == 1 {
			return hatch.GenerateResult{}, errors.New("synthetic initial failure")
		}
		return hatch.GenerateResult{Text: "retry succeeded"}, nil
	})
	if _, err := h.Chat(ctx, req, nil); err == nil {
		t.Fatal("initial generation should fail")
	}

	var sends int
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	http.DefaultTransport = telegramTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			return nil, errors.New("unexpected Bot API method")
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("text") != "retry succeeded" {
			t.Fatalf("sendMessage text=%q err=%v", r.Form.Get("text"), err)
		}
		sends++
		status, body := http.StatusOK, `{"ok":true,"result":{"message_id":100}}`
		if sends == 1 {
			status, body = http.StatusBadGateway, `{"ok":false}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	ch, err := telegram.New(map[string]any{"telegram.token": "123:synthetic", "telegram.mode": "webhook"}, h)
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
	const first = `{"update_id":91,"message":{"message_id":9,"from":{"id":12},"chat":{"id":4242},"text":"/retry"}}`
	deliver(first, http.StatusInternalServerError)
	deliver(first, http.StatusNoContent)
	deliver(first, http.StatusNoContent)
	if generations != 2 || sends != 2 {
		t.Fatalf("redelivery: generations=%d, sends=%d", generations, sends)
	}
	var history sessionRecordForTest
	if err := db.Get(ctx, "session", session, &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 2 || history.Messages[0].Content != req.Text || history.Messages[1].Content != "retry succeeded" {
		t.Fatalf("unexpected retry history: %#v", history.Messages)
	}
	const next = `{"update_id":92,"message":{"message_id":10,"from":{"id":12},"chat":{"id":4242},"text":"/retry"}}`
	deliver(next, http.StatusNoContent)
	if generations != 3 || sends != 3 {
		t.Fatalf("different message: generations=%d, sends=%d", generations, sends)
	}
}

type sessionRecordForTest struct {
	Messages []hatch.Message `json:"messages"`
}
