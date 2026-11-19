package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

func TestTelegramCatalogIsolationAndUnknownLegacyCreator(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	current, err := s.TelegramSession(ctx, -42, 7)
	if err != nil {
		t.Fatal(err)
	}
	legacyID := "telegram_-42_legacy"
	fixtures := []sessionRecord{
		{ID: legacyID, Platform: "telegram", UserID: "-42", Title: "Old topic", Messages: []hatch.Message{{Role: "user", Content: "old message"}}},
		{ID: "telegram_-420_other", Platform: "telegram", UserID: "-420", Title: "Other chat"},
		{ID: "telegram_-42_spoof", Platform: "web", UserID: "-42", Title: "Wrong platform"},
		{ID: "telegram_-42_wrong", Platform: "telegram", UserID: "-99", Title: "Wrong owner"},
	}
	for _, record := range fixtures {
		if err := db.Put(ctx, "session", record.ID, record, nil); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ListTelegramSessions(ctx, -42)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !items[0].Current || items[0].ID != current || items[1].ID != legacyID {
		t.Fatalf("sessions=%#v", items)
	}
	for _, bad := range []string{"telegram_-420_other", "telegram_-42_spoof", "telegram_-42_wrong", "telegram_-42_missing", "telegram_-42_../bad"} {
		if err := s.SwitchTelegramSession(ctx, -42, bad); !errors.Is(err, ErrTelegramSessionUnavailable) {
			t.Fatalf("switch %q error=%v", bad, err)
		}
		if _, err := s.TelegramSessionHistory(ctx, -42, bad); !errors.Is(err, ErrTelegramSessionUnavailable) {
			t.Fatalf("history %q error=%v", bad, err)
		}
	}
	if err := s.SwitchTelegramSession(ctx, -42, legacyID); err != nil {
		t.Fatal(err)
	}
	var pointer telegramSessionRecord
	if err := db.Get(ctx, "telegram_session", telegramSessionKey(-42), &pointer); err != nil {
		t.Fatal(err)
	}
	if pointer.UserID != 0 || pointer.Known[current].UserID != 7 {
		t.Fatalf("switched ownership=%#v", pointer)
	}
	if err := s.RenameTelegramSession(ctx, -42, 8, legacyID, "Must not claim"); !errors.Is(err, ErrTelegramSessionCreator) {
		t.Fatalf("rename unknown creator=%v", err)
	}
	if err := s.SwitchTelegramSession(ctx, -42, current); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameTelegramSession(ctx, -42, 7, current, "Creator restored"); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramCatalogDoesNotClaimOwnerlessDefaultLegacyHistory(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	id := telegramSessionID(-43)
	legacy := sessionRecord{ID: id, Platform: "telegram", Messages: []hatch.Message{{Role: "user", Content: "old private context"}}}
	if err := db.Put(ctx, "session", id, legacy, nil); err != nil {
		t.Fatal(err)
	}
	if selected, err := s.TelegramSession(ctx, -43, 99); err != nil || selected != id {
		t.Fatalf("selection=%q err=%v", selected, err)
	}
	if err := s.RenameTelegramSession(ctx, -43, 99, id, "Must not claim"); !errors.Is(err, ErrTelegramSessionCreator) {
		t.Fatalf("ownerless legacy rename=%v", err)
	}
	if reply, err := s.TelegramPersonas(ctx, id, "99", "_none"); err != nil || !strings.Contains(reply, "只有创建") {
		t.Fatalf("ownerless legacy persona=%q err=%v", reply, err)
	}
	var pointer telegramSessionRecord
	if err := db.Get(ctx, "telegram_session", telegramSessionKey(-43), &pointer); err != nil || pointer.UserID != 0 {
		t.Fatalf("creator=%d err=%v", pointer.UserID, err)
	}
	if history, err := s.TelegramSessionHistory(ctx, -43, id); err != nil || len(history) != 1 || history[0].Content != "old private context" {
		t.Fatalf("history=%#v err=%v", history, err)
	}
}

func TestTelegramCatalogPreservesKnownPrivateLegacyCreator(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	id := telegramSessionID(43)
	if err := db.Put(ctx, "session", id, sessionRecord{ID: id, Platform: "telegram", UserID: "43"}, nil); err != nil {
		t.Fatal(err)
	}
	if current, err := s.TelegramSession(ctx, 43, 43); err != nil || current != id {
		t.Fatalf("selection=%q err=%v", current, err)
	}
	if err := s.RenameTelegramSession(ctx, 43, 43, id, "Retained creator"); err != nil {
		t.Fatal(err)
	}
	var pointer telegramSessionRecord
	if err := db.Get(ctx, "telegram_session", telegramSessionKey(43), &pointer); err != nil || pointer.UserID != 43 {
		t.Fatalf("creator=%d err=%v", pointer.UserID, err)
	}
}

func TestTelegramCatalogEmptySessionsAndOwnershipSurviveBackup(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	first, err := s.TelegramSession(ctx, 42, 7)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.NewTelegramSession(ctx, 42, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SwitchTelegramSession(ctx, 42, first); err != nil {
		t.Fatal(err)
	}
	out, err := s.Action(ctx, "backup.export", map[string]any{"passphrase": "catalog-backup-passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testService(t)
	if _, err := target.Action(ctx, "backup.import", map[string]any{"passphrase": "catalog-backup-passphrase", "blob": out.(map[string]any)["blob"]}); err != nil {
		t.Fatal(err)
	}
	items, err := target.ListTelegramSessions(ctx, 42)
	if err != nil || len(items) != 2 || !items[0].Current || items[0].ID != first {
		t.Fatalf("restored catalog=%#v err=%v", items, err)
	}
	if err := target.RenameTelegramSession(ctx, 42, 8, first, "Wrong"); !errors.Is(err, ErrTelegramSessionCreator) {
		t.Fatalf("restored first creator=%v", err)
	}
	if err := target.RenameTelegramSession(ctx, 42, 7, first, "First topic"); err != nil {
		t.Fatal(err)
	}
	if err := target.SwitchTelegramSession(ctx, 42, second); err != nil {
		t.Fatal(err)
	}
	if err := target.RenameTelegramSession(ctx, 42, 8, second, "Second topic"); err != nil {
		t.Fatal(err)
	}
	if history, err := target.TelegramSessionHistory(ctx, 42, second); err != nil || len(history) != 0 {
		t.Fatalf("empty history=%#v err=%v", history, err)
	}
}

func TestTelegramCatalogRenameValidationAndHistoryBound(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	id, err := s.TelegramSession(ctx, 42, 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"", "\n", "a\nb", "a\u202eb"} {
		if err := s.RenameTelegramSession(ctx, 42, 7, id, title); !errors.Is(err, ErrTelegramSessionTitle) {
			t.Fatalf("title %q error=%v", title, err)
		}
	}
	s.sessionsMu.Lock()
	s.sessions[id] = func() {}
	s.sessionsMu.Unlock()
	err = s.RenameTelegramSession(ctx, 42, 7, id, "Busy")
	s.sessionsMu.Lock()
	delete(s.sessions, id)
	s.sessionsMu.Unlock()
	if !errors.Is(err, ErrTelegramSessionBusy) {
		t.Fatalf("busy rename=%v", err)
	}
	record := sessionRecord{ID: id, Platform: "telegram", UserID: "42"}
	for i := 0; i < 14; i++ {
		role := "user"
		if i%2 != 0 {
			role = "assistant"
		}
		record.Messages = append(record.Messages, hatch.Message{Role: role, Content: strconv.Itoa(i)})
	}
	record.Messages = append(record.Messages, hatch.Message{Role: "system", Content: "private preset"})
	if err := db.Put(ctx, "session", id, record, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.TelegramSessionHistory(ctx, 42, id)
	if err != nil || !reflect.DeepEqual(got, record.Messages[4:14]) {
		t.Fatalf("recent history=%#v err=%v", got, err)
	}
	if _, err := s.NewTelegramSession(ctx, 42, 8); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameTelegramSession(ctx, 42, 7, id, "Stale"); !errors.Is(err, ErrTelegramSessionUnavailable) {
		t.Fatalf("stale rename=%v", err)
	}
}

func TestTelegramCatalogResetRefusesPendingRename(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	id, err := s.TelegramSession(ctx, 42, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenameTelegramSession(ctx, 42, 7, id, "Before"); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberTelegramPrompt(ctx, id, "7", "retained retry"); err != nil {
		t.Fatal(err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.TelegramCommandOnce(ctx, id, 42, 7, 100, "rename", func() (string, error) {
			close(entered)
			<-proceed
			return "renamed", s.RenameTelegramSession(ctx, 42, 7, id, "After")
		})
		done <- err
	}()
	<-entered
	_, resetErr := s.Action(ctx, "sessions.reset", map[string]any{"id": id})
	var before sessionRecord
	readErr := db.Get(ctx, "session", id, &before)
	prompt, retryErr := s.TelegramLastPrompt(ctx, id, "7")
	close(proceed)
	commandErr := <-done
	if resetErr == nil || readErr != nil || before.Title != "Before" || retryErr != nil || prompt != "retained retry" {
		t.Fatalf("reset during rename: reset=%v read=%v title=%q retry=%q/%v", resetErr, readErr, before.Title, prompt, retryErr)
	}
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	if _, err := s.Action(ctx, "sessions.reset", map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(ctx, "session", id, &before); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("history survived reset: %v", err)
	}
}

func TestTelegramCatalogBackupRejectsForeignCatalogBeforeWrites(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	pointer := telegramSessionRecord{ChatID: 42, UserID: 7, SessionID: "telegram_42", Known: map[string]telegramSessionOwner{"telegram_99_other": {UserID: 7}}}
	data, err := json.Marshal(pointer)
	if err != nil {
		t.Fatal(err)
	}
	row := backupRecord{Kind: "telegram_session", ID: "chat_42", Data: data}
	if err := validateTelegramBackupRecord(row); err == nil {
		t.Fatal("foreign catalog entry accepted")
	}
	plain, err := json.Marshal(backupEnvelope{Version: 1, Records: []backupRecord{
		{Kind: "settings", ID: "catalog-marker", Data: json.RawMessage(`{"retained":true}`)}, row,
	}})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := secure.BackupEncrypt("catalog-backup-passphrase", plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "backup.import", map[string]any{"passphrase": "catalog-backup-passphrase", "blob": blob}); err == nil {
		t.Fatal("invalid catalog backup accepted")
	}
	var marker map[string]any
	if err := db.Get(ctx, "settings", "catalog-marker", &marker); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid backup wrote earlier marker: %#v err=%v", marker, err)
	}
}
