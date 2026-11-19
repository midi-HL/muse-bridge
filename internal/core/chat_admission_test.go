package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

const admissionTestTimeout = 2 * time.Second

func waitAdmissionSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(admissionTestTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitAdmissionResult(t *testing.T, ch <-chan error, want error, what string) {
	t.Helper()
	select {
	case err := <-ch:
		if !errors.Is(err, want) {
			t.Fatalf("%s error = %v, want %v", what, err, want)
		}
	case <-time.After(admissionTestTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestChatAdmissionQueueZeroRejectsImmediately(t *testing.T) {
	a := newChatAdmission(1, 0)
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := a.acquire(context.Background(), nil); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second acquire error = %v, want ErrQueueFull", err)
	}
	if active, queued := a.snapshot(); active != 1 || queued != 0 {
		t.Fatalf("snapshot after rejection = (%d, %d), want (1, 0)", active, queued)
	}
	a.release()
	if active, queued := a.snapshot(); active != 0 || queued != 0 {
		t.Fatalf("snapshot after release = (%d, %d), want (0, 0)", active, queued)
	}
}

func TestChatAdmissionCancelledContextDoesNotReserveAvailableSlot(t *testing.T) {
	a := newChatAdmission(1, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.acquire(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire with cancelled context = %v, want context.Canceled", err)
	}
	if active, queued := a.snapshot(); active != 0 || queued != 0 {
		t.Fatalf("snapshot after cancelled acquire = (%d, %d), want (0, 0)", active, queued)
	}
}

func TestChatAdmissionCancellationRemovesWaiter(t *testing.T) {
	a := newChatAdmission(1, 2)
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queued := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- a.acquire(ctx, func() error {
			close(queued)
			return nil
		})
	}()
	waitAdmissionSignal(t, queued, "waiter enqueue")
	if active, waiting := a.snapshot(); active != 1 || waiting != 1 {
		t.Fatalf("queued snapshot = (%d, %d), want (1, 1)", active, waiting)
	}

	cancel()
	waitAdmissionResult(t, result, context.Canceled, "cancelled acquire")
	if active, waiting := a.snapshot(); active != 1 || waiting != 0 {
		t.Fatalf("snapshot after cancellation = (%d, %d), want (1, 0)", active, waiting)
	}

	a.release()
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatalf("slot was not reusable after cancellation: %v", err)
	}
	a.release()
}

func TestChatAdmissionCancellationAfterHandoffKeepsCallerSlot(t *testing.T) {
	a := newChatAdmission(1, 1)
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queued := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- a.acquire(ctx, func() error {
			close(queued)
			return nil
		})
	}()
	waitAdmissionSignal(t, queued, "waiter enqueue")
	a.update(2, 1)
	waitAdmissionResult(t, result, nil, "granted acquire")

	// Cancellation after successful handoff must not reclaim a slot now owned
	// by the caller; only its matching release does that.
	cancel()
	if active, queued := a.snapshot(); active != 2 || queued != 0 {
		t.Fatalf("snapshot after post-handoff cancellation = (%d, %d), want (2, 0)", active, queued)
	}
	a.release()
	a.release()
	if active, queued := a.snapshot(); active != 0 || queued != 0 {
		t.Fatalf("final snapshot = (%d, %d), want (0, 0)", active, queued)
	}
}

func TestChatAdmissionCancellationReclaimsGrantBeforeHandoff(t *testing.T) {
	a := newChatAdmission(1, 1)
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifyEntered := make(chan struct{})
	finishNotify := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- a.acquire(ctx, func() error {
			close(notifyEntered)
			<-finishNotify
			return nil
		})
	}()
	waitAdmissionSignal(t, notifyEntered, "queue notification")

	// Grant while acquire is still in the lock-free callback, then cancel before
	// it can transfer ownership of that grant to the caller.
	a.update(2, 1)
	cancel()
	close(finishNotify)
	waitAdmissionResult(t, result, context.Canceled, "cancelled granted acquire")
	if active, queued := a.snapshot(); active != 1 || queued != 0 {
		t.Fatalf("snapshot after cancelled grant = (%d, %d), want (1, 0)", active, queued)
	}

	a.release()
	if active, queued := a.snapshot(); active != 0 || queued != 0 {
		t.Fatalf("final snapshot = (%d, %d), want (0, 0)", active, queued)
	}
}

func TestChatAdmissionNotifyFailureReleasesConcurrentGrant(t *testing.T) {
	a := newChatAdmission(1, 1)
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	notifyEntered := make(chan struct{})
	finishNotify := make(chan struct{})
	result := make(chan error, 1)
	var calls atomic.Int32
	notifyErr := errors.New("queue notification failed")
	go func() {
		result <- a.acquire(context.Background(), func() error {
			calls.Add(1)
			close(notifyEntered)
			<-finishNotify
			return notifyErr
		})
	}()
	waitAdmissionSignal(t, notifyEntered, "queue notification")
	if active, queued := a.snapshot(); active != 1 || queued != 1 {
		t.Fatalf("snapshot before update = (%d, %d), want (1, 1)", active, queued)
	}

	// The waiter is granted while its lock-free notification is still running.
	a.update(2, 1)
	if active, queued := a.snapshot(); active != 2 || queued != 0 {
		t.Fatalf("snapshot after grant = (%d, %d), want (2, 0)", active, queued)
	}
	close(finishNotify)
	waitAdmissionResult(t, result, notifyErr, "failed notification")
	if calls.Load() != 1 {
		t.Fatalf("onQueued calls = %d, want 1", calls.Load())
	}
	if active, queued := a.snapshot(); active != 1 || queued != 0 {
		t.Fatalf("snapshot after failed notification = (%d, %d), want (1, 0)", active, queued)
	}

	// The slot reserved for the failed waiter was returned, not leaked.
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatalf("acquire after failed notification = %v", err)
	}
	a.release()
	a.release()
}

func TestChatAdmissionRaisesLimitInFIFOOrder(t *testing.T) {
	a := newChatAdmission(1, 3)
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	aQueued, bQueued := make(chan struct{}), make(chan struct{})
	allowA, allowB := make(chan struct{}), make(chan struct{})
	aResult, bResult := make(chan error, 1), make(chan error, 1)
	go func() {
		aResult <- a.acquire(context.Background(), func() error {
			close(aQueued)
			<-allowA
			return nil
		})
	}()
	waitAdmissionSignal(t, aQueued, "first waiter enqueue")
	go func() {
		bResult <- a.acquire(context.Background(), func() error {
			close(bQueued)
			<-allowB
			return nil
		})
	}()
	waitAdmissionSignal(t, bQueued, "second waiter enqueue")

	a.update(2, 3)
	close(allowA)
	waitAdmissionResult(t, aResult, nil, "first FIFO waiter")
	if active, queued := a.snapshot(); active != 2 || queued != 1 {
		t.Fatalf("snapshot after first grant = (%d, %d), want (2, 1)", active, queued)
	}

	// Returning the original active slot must grant the second waiter next.
	a.release()
	close(allowB)
	waitAdmissionResult(t, bResult, nil, "second FIFO waiter")
	if active, queued := a.snapshot(); active != 2 || queued != 0 {
		t.Fatalf("snapshot after second grant = (%d, %d), want (2, 0)", active, queued)
	}
	a.release()
	a.release()
	if active, queued := a.snapshot(); active != 0 || queued != 0 {
		t.Fatalf("final snapshot = (%d, %d), want (0, 0)", active, queued)
	}
}

func TestChatAdmissionLoweringWaitsForActiveToDrainAndKeepsWaiters(t *testing.T) {
	a := newChatAdmission(1, 2)
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	firstQueued, secondQueued := make(chan struct{}), make(chan struct{})
	firstResult, secondResult := make(chan error, 1), make(chan error, 1)
	go func() {
		firstResult <- a.acquire(context.Background(), func() error {
			close(firstQueued)
			return nil
		})
	}()
	waitAdmissionSignal(t, firstQueued, "first waiter enqueue")
	go func() {
		secondResult <- a.acquire(context.Background(), func() error {
			close(secondQueued)
			return nil
		})
	}()
	waitAdmissionSignal(t, secondQueued, "second waiter enqueue")

	a.update(1, 0)
	if err := a.acquire(context.Background(), nil); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("new acquire after shrinking queue = %v, want ErrQueueFull", err)
	}
	if active, queued := a.snapshot(); active != 1 || queued != 2 {
		t.Fatalf("snapshot after shrink = (%d, %d), want (1, 2)", active, queued)
	}

	a.release()
	waitAdmissionResult(t, firstResult, nil, "first retained waiter")
	if active, queued := a.snapshot(); active != 1 || queued != 1 {
		t.Fatalf("snapshot with first active = (%d, %d), want (1, 1)", active, queued)
	}
	a.release()
	waitAdmissionResult(t, secondResult, nil, "second retained waiter")
	if active, queued := a.snapshot(); active != 1 || queued != 0 {
		t.Fatalf("snapshot with second active = (%d, %d), want (1, 0)", active, queued)
	}
	a.release()
}

func TestChatAdmissionClampsLimits(t *testing.T) {
	a := newChatAdmission(0, -1)
	if err := a.acquire(context.Background(), nil); err != nil {
		t.Fatalf("minimum concurrency was not enforced: %v", err)
	}
	if err := a.acquire(context.Background(), nil); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("negative queue size was not clamped to zero: %v", err)
	}
	a.update(0, -1)
	a.release()
	if active, queued := a.snapshot(); active != 0 || queued != 0 {
		t.Fatalf("snapshot after clamped update = (%d, %d), want (0, 0)", active, queued)
	}
}
