package core

import (
	"context"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/midi-HL/muse-bridge/internal/store"
)

func TestTelegramUsageReadsSharedQuotaWithoutCharging(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	seed, _ := testService(t)
	db, err := store.Open(dir, seed.vault)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s, err := New(ctx, db, seed.vault, seed.redactor, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	// Keep the real charging path away from a live midnight boundary.
	zone := "UTC"
	if hour := time.Now().UTC().Hour(); hour >= 23 || hour == 0 {
		zone = "Asia/Kuala_Lumpur"
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 4, "general.timezone": zone}); err != nil {
		t.Fatal(err)
	}
	for _, sender := range []string{"7", "0"} {
		if err := s.chargeDailyChat(ctx, ChatRequest{Platform: "telegram", UserID: "-42", RetryUserID: sender}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dir, seed.vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err = New(ctx, db, seed.vault, seed.redactor, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, tc := range []struct {
		chat, sender int64
		used         int
	}{{-99, 7, 1}, {-42, 8, 0}, {-42, 0, 1}, {-99, 0, 0}, {-42, 7, 1}, {-42, 7, 1}} {
		u, err := s.TelegramUsage(ctx, tc.chat, tc.sender)
		if err != nil || u.Used != tc.used || u.Limit != 4 || u.Timezone != zone {
			t.Fatalf("chat %d sender %d usage=%+v err=%v, want used=%d limit=4 zone=%s", tc.chat, tc.sender, u, err, tc.used, zone)
		}
	}
}

func TestTelegramUsageSeparatesBucketsAtLocalDateChange(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.Action(ctx, "settings.update", map[string]any{"general.timezone": "Asia/Kuala_Lumpur"}); err != nil {
		t.Fatal(err)
	}
	subject, err := chatDailySubject(ChatRequest{Platform: "telegram", UserID: "-42", RetryUserID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC)
	before := reset.Add(-time.Nanosecond)
	bucket, err := chatDailyPeriod(s.Settings(), before)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Increment(ctx, "chat_day", subject, bucket, 0); err != nil {
		t.Fatal(err)
	}
	u, err := s.telegramUsageAt(ctx, -42, 7, before)
	if err != nil || u.Used != 1 || !u.ResetsAt.Equal(reset) {
		t.Fatalf("before reset: usage=%+v err=%v", u, err)
	}
	u, err = s.telegramUsageAt(ctx, -42, 7, reset)
	if err != nil || u.Used != 0 || !u.ResetsAt.Equal(reset.AddDate(0, 0, 1)) {
		t.Fatalf("at reset: usage=%+v err=%v", u, err)
	}
}

func TestTelegramUsageDoesNotHideStorageError(t *testing.T) {
	s, db := testService(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TelegramUsage(context.Background(), -42, 7); err == nil {
		t.Fatal("counter storage error was hidden")
	}
}
