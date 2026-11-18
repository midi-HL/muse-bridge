package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNodeProbeRejectsInvalidURLAndPersistsSafeError(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if _, err := s.Action(ctx, "nodes.save", map[string]any{
		"id": "bad-node", "name": "Bad", "url": "ftp://user:secret@example.invalid:21", "enabled": true,
	}); err == nil {
		t.Fatal("unsupported node URL was accepted")
	}
	if _, err := s.Action(ctx, "nodes.save", map[string]any{
		"id": "bad-node", "name": "Bad", "url": "http://user:secret@127.0.0.1:1", "enabled": true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Action(ctx, "nodes.test", map[string]any{"id": "bad-node"})
	if err == nil || !strings.Contains(err.Error(), "node probe failed") {
		t.Fatalf("expected safe probe failure, got %#v %v", got, err)
	}
	var item nodeRecord
	if err := db.Get(ctx, "node", "bad-node", &item); err != nil {
		t.Fatal(err)
	}
	if item.Status != "error" || item.TestedAt == "" || strings.Contains(item.Error, "secret") {
		t.Fatalf("unsafe probe record: %#v", item)
	}
	if _, err := s.Action(ctx, "nodes.test", map[string]any{"id": "missing"}); err == nil || err.Error() != "node not found" {
		t.Fatalf("missing node error: %v", err)
	}
}

func TestCronMatcherAndSchedulerCallback(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Kuala_Lumpur")
	matched := time.Date(2026, time.January, 2, 9, 0, 0, 0, loc)
	if !cronMatches("0 9 * * 5", matched) || cronMatches("1 9 * * 5", matched) {
		t.Fatal("cron matcher returned unexpected result")
	}
	if validCron("0 9 * * *") == false || validCron("every day") || validCron("0 9 0 * *") {
		t.Fatal("cron validator accepted invalid expression")
	}

	s, _ := testService(t)
	if _, err := s.Action(context.Background(), "schedules.save", map[string]any{
		"id": "due", "name": "Due", "platform": "web", "target": "target", "prompt": "hello",
		"cron": "0 9 * * *", "timezone": "Asia/Kuala_Lumpur", "enabled": true,
	}); err != nil {
		t.Fatal(err)
	}
	called := make(chan ScheduleExecution, 1)
	s.SetScheduleCallback(func(item ScheduleExecution, _ time.Time) { called <- item })
	s.runSchedulerOnce(matched)
	select {
	case item := <-called:
		if item.ID != "due" || item.Prompt != "hello" {
			t.Fatalf("unexpected callback payload: %#v", item)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler callback did not run")
	}
	select {
	case <-called:
		t.Fatal("scheduler duplicated execution in the same minute")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSchedulerStopsWithServiceClose(t *testing.T) {
	s, _ := testService(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.life.Done():
	case <-time.After(time.Second):
		t.Fatal("service context did not stop")
	}
}
