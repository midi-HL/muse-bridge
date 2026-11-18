package main

import (
	"context"
	"encoding/base64"
	"errors"
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

type sessionRecordForTest struct {
	Messages []hatch.Message `json:"messages"`
}
