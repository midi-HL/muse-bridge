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
func TestTelegramCommandDeliveryIsDeduplicatedAndRetriesFailedSend(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	session := "telegram_42"
	if err := db.Put(ctx, "session", session, sessionRecord{ID: session, Platform: "telegram", UserID: "42"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TelegramCommandOnce(ctx, session, 42, 7, 9, "retry", func() (string, error) { return "reply", nil }); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	_, err := s.TelegramCommandDeliver(ctx, 42, 7, 9, "retry", func() error { attempts.Add(1); return errors.New("send failed") })
	if err == nil {
		t.Fatal("failed send accepted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.TelegramCommandDeliver(ctx, 42, 7, 9, "retry", func() error { attempts.Add(1); return nil })
			if e != nil {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("successful sends were duplicated or failed retry suppressed: calls=%d", got)
	}
	reopened, err := New(ctx, db, s.vault, s.redactor, s.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, err = reopened.TelegramCommandDeliver(ctx, 42, 7, 9, "retry", func() error { attempts.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatal("persisted delivery marker did not suppress restart replay")
	}
}

func TestTelegramCommandDeliveryIsIdempotentAndRetriesFailure(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	session := "telegram_42"
	if err := db.Put(ctx, "session", session, sessionRecord{ID: session, Platform: "telegram", UserID: "42"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TelegramCommandOnce(ctx, session, 42, 7, 9, "retry", func() (string, error) { return "reply", nil }); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	_, err := s.TelegramCommandDeliver(ctx, 42, 7, 9, "retry", func() error { calls.Add(1); return errors.New("send failed") })
	if err == nil {
		t.Fatal("failed delivery accepted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.TelegramCommandDeliver(ctx, 42, 7, 9, "retry", func() error { calls.Add(1); return nil })
			if e != nil {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatalf("delivery calls=%d, want failed send plus one success", calls.Load())
	}
	reopened, err := New(ctx, db, s.vault, s.redactor, s.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, err = reopened.TelegramCommandDeliver(ctx, 42, 7, 9, "retry", func() error { calls.Add(1); return nil })
	if err != nil || calls.Load() != 2 {
		t.Fatal("successful command reply was sent again after restart")
	}
}

func TestLiveCommandDeliveryDoesNotSuppressCompletedReply(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	session := "telegram_42"
	entered, finish := make(chan struct{}), make(chan struct{})
	commandDone := make(chan error, 1)
	go func() {
		_, err := s.TelegramCommandOnce(ctx, session, 42, 7, 12, "retry", func() (string, error) { close(entered); <-finish; return "final result", nil })
		commandDone <- err
	}()
	<-entered
	var attempts atomic.Int32
	handled, err := s.TelegramCommandDeliver(ctx, 42, 7, 12, "retry", func() error { attempts.Add(1); return nil })
	close(finish)
	if err != nil || !handled || attempts.Load() != 0 {
		t.Fatalf("live command delivery: handled=%v attempts=%d err=%v", handled, attempts.Load(), err)
	}
	if err := <-commandDone; err != nil {
		t.Fatal(err)
	}
	_, err = s.TelegramCommandDeliver(ctx, 42, 7, 12, "retry", func() error { attempts.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("pending notice/final reply delivered %d times", attempts.Load())
	}
	var record telegramCommandRecord
	key := telegramCommandKey(42, 12)
	if err = db.Get(ctx, "telegram_command", key, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != "done" || !record.Delivered {
		t.Fatalf("final command state=%#v", record)
	}
}

func TestResetRefusesDuringTelegramReplyDeliveryWithoutPartialMutation(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	session := "telegram_42"
	if err := db.Put(ctx, "session", session, sessionRecord{ID: session, Platform: "telegram", UserID: "42"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberTelegramPrompt(ctx, session, "7", "retry this"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TelegramCommandOnce(ctx, session, 42, 7, 3, "retry", func() (string, error) { return "reply", nil }); err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	deliverDone := make(chan error, 1)
	go func() {
		_, err := s.TelegramCommandDeliver(ctx, 42, 7, 3, "retry", func() error { close(entered); <-finish; return nil })
		deliverDone <- err
	}()
	<-entered
	if _, err := s.Action(ctx, "sessions.reset", map[string]any{"id": session}); err == nil {
		t.Fatal("reset succeeded during active command delivery")
	}
	if prompt, err := s.TelegramLastPrompt(ctx, session, "7"); err != nil || prompt != "retry this" {
		t.Fatal("failed reset partially deleted retry state")
	}
	close(finish)
	if err := <-deliverDone; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "sessions.reset", map[string]any{"id": session}); err != nil {
		t.Fatal(err)
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
