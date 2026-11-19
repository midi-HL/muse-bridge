package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func TestChatAdmissionUpdatesConcurrencyAndReportsQueueStatus(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	if _, err := s.Action(ctx, "accounts.import", map[string]any{"id": "admission", "cookies": map[string]string{"hatch_sess": "synthetic"}}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	var modelCalls atomic.Int32
	s.generate = func(run context.Context, _ *hatch.Client, _ hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
		modelCalls.Add(1)
		started <- struct{}{}
		<-run.Done()
		return hatch.GenerateResult{}, run.Err()
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"limits.concurrency": 2, "limits.queue_size": 1}); err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(ctx)
	firstDone := make(chan error, 1)
	go func() {
		_, err := s.Chat(firstCtx, ChatRequest{SessionID: "active-1", Platform: "web", UserID: "admin", Prompt: "one"}, nil)
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first model call not started")
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"limits.concurrency": 1, "limits.queue_size": 1}); err != nil {
		t.Fatal(err)
	}
	secondCtx, cancelSecond := context.WithCancel(ctx)
	defer cancelSecond()
	status := make(chan ChatEvent, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := s.Chat(secondCtx, ChatRequest{SessionID: "active-2", Platform: "web", UserID: "admin", Prompt: "two"}, func(e ChatEvent) error {
			if e.Type == "queued" {
				status <- e
			}
			return nil
		})
		secondDone <- err
	}()
	select {
	case event := <-status:
		if event.Text == "" {
			t.Fatal("queue status has no message")
		}
	case <-time.After(time.Second):
		t.Fatal("queued chat did not expose queue status")
	}
	if calls := modelCalls.Load(); calls != 1 {
		t.Fatalf("model active calls=%d want 1", calls)
	}
	cancelSecond()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation=%v", err)
	}
	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("active cancellation=%v", err)
	}
	if active, queued := s.admission.snapshot(); active != 0 || queued != 0 {
		t.Fatalf("admission leaked active=%d queued=%d", active, queued)
	}
	if modelCalls.Load() != 1 {
		t.Fatalf("model calls=%d", modelCalls.Load())
	}
}
