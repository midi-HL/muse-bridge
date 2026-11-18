package main

import (
	"context"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/core"
)

func TestScheduledTelegramTargetValidation(t *testing.T) {
	r := newTelegramRuntime(context.Background(), nil)
	if err := r.sendScheduled(context.Background(), core.ScheduleExecution{Platform: "telegram", Target: "not-a-chat-id", Prompt: "hello"}); err == nil {
		t.Fatal("invalid target accepted")
	}
	if err := r.sendScheduled(context.Background(), core.ScheduleExecution{Platform: "web", Target: "123", Prompt: "hello"}); err == nil {
		t.Fatal("unsupported platform accepted")
	}
}
