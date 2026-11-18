package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTelegramCommandDuplicateReusesPersistedResult(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	var calls atomic.Int32
	fn := func() (string, error) { calls.Add(1); return "generated only once", nil }
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text, err := s.TelegramCommandOnce(ctx, "telegram_42", 42, 7, 99, "retry", fn)
			if err != nil {
				errs <- err
			} else if text != "generated only once" {
				errs <- errors.New("cached result mismatch")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("generation calls=%d", calls.Load())
	}
	reopened, err := New(ctx, db, s.vault, s.redactor, s.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	text, err := reopened.TelegramCommandOnce(ctx, "telegram_other", 42, 7, 99, "retry", fn)
	if err != nil || text != "generated only once" || calls.Load() != 1 {
		t.Fatal("restart redelivery regenerated result")
	}
	if _, err = reopened.TelegramCommandOnce(ctx, "telegram_42", 42, 8, 99, "retry", fn); err == nil {
		t.Fatal("foreign sender reused command identity")
	}
}
func TestResetInvalidatesStoredTelegramCommandResponse(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if err := db.Put(ctx, "session", "telegram_42", sessionRecord{ID: "telegram_42", Platform: "telegram", UserID: "42"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TelegramCommandOnce(ctx, "telegram_42", 42, 7, 1, "retry", func() (string, error) { return "old answer", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "sessions.reset", map[string]any{"id": "telegram_42"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	text, err := s.TelegramCommandOnce(ctx, "telegram_42", 42, 7, 1, "retry", func() (string, error) { calls++; return "new", nil })
	if err != nil || calls != 0 || text == "old answer" {
		t.Fatal("reset replayed removed command output")
	}
}
func TestResetInvalidatesPendingTelegramCommandCompletion(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if err := db.Put(ctx, "session", "telegram_42", sessionRecord{ID: "telegram_42", Platform: "telegram", UserID: "42"}, nil); err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	done := make(chan string, 1)
	go func() {
		text, _ := s.TelegramCommandOnce(ctx, "telegram_42", 42, 7, 2, "retry", func() (string, error) { close(entered); <-finish; return "stale", nil })
		done <- text
	}()
	<-entered
	if _, err := s.Action(ctx, "sessions.reset", map[string]any{"id": "telegram_42"}); err != nil {
		t.Fatal(err)
	}
	close(finish)
	if text := <-done; text == "stale" {
		t.Fatal("in-flight completion overwrote reset tombstone")
	}
}
