package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func installQuotaTestGenerator(t *testing.T, s *Service) *int {
	t.Helper()
	if _, err := s.Action(context.Background(), "accounts.import", map[string]any{"id": "quota-test", "cookies": map[string]string{"hatch_sess": "synthetic"}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.generate = func(context.Context, *hatch.Client, hatch.GenerateRequest, func(hatch.Event) error) (hatch.GenerateResult, error) {
		calls++
		return hatch.GenerateResult{Text: "ok"}, nil
	}
	return &calls
}

func TestChatDailyQuotaCountsTrustedIdentityAndRejectsHistory(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	calls := installQuotaTestGenerator(t, s)
	if _, err := s.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "quota-first", Platform: "web", UserID: "admin", Prompt: "first"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "quota-second", Platform: "web", UserID: "admin", Prompt: "second"}, nil); !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("second request error=%v", err)
	}
	if *calls != 1 {
		t.Fatalf("generation calls=%d, want 1", *calls)
	}
	var rejected sessionRecord
	if err := db.Get(ctx, "session", "quota-second", &rejected); err == nil {
		t.Fatal("quota-rejected request wrote history")
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "quota-api", Platform: "api", UserID: "admin", Prompt: "separate platform"}, nil); err != nil {
		t.Fatalf("platform quota was not isolated: %v", err)
	}
}

func TestChatLocalValidationDoesNotConsumeDailyQuota(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	installQuotaTestGenerator(t, s)
	if _, err := s.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 1}); err != nil {
		t.Fatal(err)
	}
	tooLong := []hatch.Message{{Role: "user", Content: strings.Repeat("x", 220<<10)}, {Role: "user", Content: strings.Repeat("y", 220<<10)}, {Role: "user", Content: strings.Repeat("z", 220<<10)}, {Role: "user", Content: strings.Repeat("a", 220<<10)}, {Role: "user", Content: strings.Repeat("b", 220<<10)}}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "long-prompt", Platform: "api", UserID: "key", Messages: tooLong}, nil); err == nil {
		t.Fatal("oversized assembled prompt accepted")
	}
	bad := [][]Upload{{{Filename: "a", MIME: "text/plain", DataBase64: "!"}}, {{Filename: strings.Repeat("n", 1025), MIME: "text/plain", DataBase64: "eA=="}}, {{Filename: "a", MIME: strings.Repeat("m", 257), DataBase64: "eA=="}}}
	for i, uploads := range bad {
		if _, err := s.Chat(ctx, ChatRequest{SessionID: "bad-upload-" + string(rune('a'+i)), Platform: "api", UserID: "key", Prompt: "invalid", Attachments: uploads}, nil); err == nil {
			t.Fatalf("invalid upload %d accepted", i)
		}
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "valid-after-invalid", Platform: "api", UserID: "key", Prompt: "valid"}, nil); err != nil {
		t.Fatalf("local validation consumed quota: %v", err)
	}
}

func TestChatDailyQuotaPersistenceZeroAndFailureCharge(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	installQuotaTestGenerator(t, s)
	if _, err := s.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "persist-one", Platform: "web", UserID: "admin", Prompt: "one"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(ctx, db, s.vault, s.redactor, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	installQuotaTestGenerator(t, reopened)
	if _, err := reopened.Chat(ctx, ChatRequest{SessionID: "persist-two", Platform: "web", UserID: "admin", Prompt: "two"}, nil); !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("reopened quota=%v", err)
	}
	if _, err := reopened.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Chat(ctx, ChatRequest{SessionID: "zero-one", Platform: "web", UserID: "admin", Prompt: "zero"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Chat(ctx, ChatRequest{SessionID: "zero-two", Platform: "web", UserID: "admin", Prompt: "still blocked"}, nil); !errors.Is(err, ErrDailyLimit) {
		t.Fatalf("unlimited period reset usage: %v", err)
	}
}

func TestChatDailyQuotaSubjectsAndConcurrentLimit(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	if _, err := s.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 2}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []ChatRequest{{Platform: "web", UserID: "admin"}, {Platform: "api", UserID: "admin"}, {Platform: "telegram", UserID: "-42", RetryUserID: "7"}, {Platform: "telegram", UserID: "-42", RetryUserID: "8"}, {Platform: "telegram", UserID: "-43", RetryUserID: "0"}} {
		if err := s.chargeDailyChat(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	accepted, limited := 0, 0
	for i := 0; i < 8; i++ {
		err := s.chargeDailyChat(ctx, ChatRequest{Platform: "web", UserID: "parallel"})
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrDailyLimit) {
			limited++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 2 || limited != 6 {
		t.Fatalf("accepted=%d limited=%d", accepted, limited)
	}
}

func TestChatQuotaPeriodMatchesConfiguredTimezoneIncludingEmptyUTC(t *testing.T) {
	instant := time.Date(2026, 11, 19, 0, 30, 0, 0, time.UTC)
	for _, tc := range []struct{ zone, want string }{{"", "UTC:2026-11-19"}, {"Asia/Kuala_Lumpur", "Asia/Kuala_Lumpur:2026-11-19"}, {"America/Los_Angeles", "America/Los_Angeles:2026-11-18"}} {
		got, err := chatDailyPeriod(map[string]any{"general.timezone": tc.zone}, instant)
		if err != nil || got != tc.want {
			t.Fatalf("zone %q period=%q err=%v want=%q", tc.zone, got, err, tc.want)
		}
	}
}

func TestChatQueueCancellationDoesNotConsumeQuota(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	installQuotaTestGenerator(t, s)
	if _, err := s.Action(ctx, "settings.update", map[string]any{"limits.concurrency": 1, "limits.queue_size": 1, "limits.daily_per_user": 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithCancel(ctx)
	queued := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.Chat(waitCtx, ChatRequest{SessionID: "cancelled", Platform: "web", UserID: "admin", Prompt: "wait"}, func(ev ChatEvent) error {
			if ev.Type == "queued" {
				close(queued)
			}
			return nil
		})
		done <- err
	}()
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("request did not queue")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	s.release()
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "after-cancel", Platform: "web", UserID: "admin", Prompt: "allowed"}, nil); err != nil {
		t.Fatalf("cancelled waiter consumed quota: %v", err)
	}
}

func TestChatQuotaSentinelRemainsStoreLimit(t *testing.T) {
	if !errors.Is(ErrDailyLimit, store.ErrLimit) {
		t.Fatal("daily limit must map to store limit")
	}
}
