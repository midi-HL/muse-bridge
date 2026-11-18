package core

import (
	"context"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"testing"
)

func TestLegacyTelegramGroupHistoryMigratesToChatOwner(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	id := "telegram_-10042"
	legacy := sessionRecord{ID: id, Platform: "telegram", UserID: "7", Messages: []hatch.Message{{Role: "user", Content: "old question"}, {Role: "assistant", Content: "old answer"}}}
	if err := db.Put(ctx, "session", id, legacy, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.TelegramSession(ctx, -10042, 99)
	if err != nil || got != id {
		t.Fatalf("session=%q err=%v", got, err)
	}
	var pointer telegramSessionRecord
	if err = db.Get(ctx, "telegram_session", telegramSessionKey(-10042), &pointer); err != nil || pointer.UserID != 7 {
		t.Fatalf("creator=%#v err=%v", pointer, err)
	}
	var migrated sessionRecord
	if err = db.Get(ctx, "session", id, &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.UserID != "-10042" || len(migrated.Messages) != 2 {
		t.Fatalf("legacy history not migrated: %#v", migrated)
	}
}
