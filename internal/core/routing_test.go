package core

import (
	"context"
	"errors"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/pool"
)

func TestChatUsesSavedPoolSelection(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	requests := captureGeneration(t, s)
	if _, err := s.Action(ctx, "accounts.update", map[string]any{"id": "synthetic", "pool": "api", "note": "API only"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"pool.split": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "web-no-pool", Platform: "web", UserID: "admin", Prompt: "hello"}, nil); !errors.Is(err, pool.ErrNoAccount) {
		t.Fatalf("web used API pool: %v", err)
	}
	var history sessionRecord
	if err := db.Get(ctx, "session", "web-no-pool", &history); err == nil {
		t.Fatal("unroutable request persisted history")
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "api-has-pool", Platform: "api", UserID: "key", Prompt: "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 1 {
		t.Fatal("split routing called incorrect generator")
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"pool.split": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "web-shared", Platform: "web", UserID: "admin", Prompt: "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 2 {
		t.Fatal("unsplit routing not restored")
	}
}
