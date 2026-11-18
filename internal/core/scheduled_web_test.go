package core

import (
	"context"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func TestDeliverScheduledWebAppendsToOwnedWebSession(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	item := sessionRecord{ID: "web-session", Platform: "web", UserID: "admin", Messages: []hatch.Message{{Role: "user", Content: "hello"}}}
	if err := db.Put(ctx, "session", item.ID, item, nil); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	if err := s.DeliverScheduledWeb(ctx, ScheduleExecution{ID: "schedule-1", Platform: "web", Target: item.ID, Prompt: "Scheduled update"}, when); err != nil {
		t.Fatal(err)
	}
	var got sessionRecord
	if err := db.Get(ctx, "session", item.ID, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[1].Role != "assistant" || got.Messages[1].Content != "Scheduled update" {
		t.Fatalf("scheduled message missing: %#v", got.Messages)
	}
	if got.Platform != "web" || got.UserID != "admin" {
		t.Fatalf("session ownership changed: %#v", got)
	}
	if got.UpdatedAt != when.Format(time.RFC3339Nano) {
		t.Fatalf("timestamp not recorded: %q", got.UpdatedAt)
	}
}

func TestDeliverScheduledWebRejectsNonWebSession(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	item := sessionRecord{ID: "telegram-session", Platform: "telegram", UserID: "42"}
	if err := db.Put(ctx, "session", item.ID, item, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverScheduledWeb(ctx, ScheduleExecution{Platform: "web", Target: item.ID, Prompt: "do not inject"}, time.Now()); err == nil {
		t.Fatal("non-Web session accepted")
	}
}
