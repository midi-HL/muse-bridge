package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/secure"
)

func TestBackupImportRejectsMalformedTelegramCommandBeforeWriting(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	passphrase := "telegram-backup-validation-pass"
	const markerID = "telegram-validation-marker"

	plain, err := json.Marshal(backupEnvelope{
		Version: 1,
		Records: []backupRecord{
			{Kind: "settings", ID: markerID, Data: json.RawMessage(`{"marker":true}`)},
			{Kind: "telegram_command", ID: "tg_bad-command", Data: json.RawMessage(`[]`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := secure.BackupEncrypt(passphrase, plain)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Action(ctx, "backup.import", map[string]any{"passphrase": passphrase, "blob": blob}); err == nil {
		t.Fatal("backup.import accepted telegram_command array data")
	}
	var marker json.RawMessage
	if err := db.Get(ctx, "settings", markerID, &marker); err == nil {
		t.Fatalf("backup.import wrote earlier record before rejecting malformed Telegram command: %s", marker)
	}
}

func TestValidateTelegramBackupRecordAcceptsProducerRecords(t *testing.T) {
	valid := []backupRecord{
		{Kind: "telegram_session", ID: "chat_-100123", Data: json.RawMessage(`{"chat_id":-100123,"user_id":77,"session_id":"telegram_-100123_ab_cd","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)},
		{Kind: "telegram_session", ID: "chat_-100124", Data: json.RawMessage(`{"chat_id":-100124,"user_id":0,"session_id":"telegram_-100124"}`)},
		{Kind: "telegram_retry", ID: telegramRetryKey("telegram_42", "sender-1"), Data: json.RawMessage(`{"session_id":"telegram_42","owner":"sender-1","attempt_id":"attempt","prompt":"retry me","committed":false,"updated_at":"2026-01-01T00:00:00Z"}`)},
		{Kind: "telegram_command", ID: "tg_" + secure.Digest("42:17")[:40], Data: json.RawMessage(`{"session_id":"telegram_42","chat_id":42,"user_id":7,"message_id":17,"command":"retry","status":"done","text":"cached","updated_at":"2026-01-01T00:00:00Z"}`)},
	}
	for _, row := range valid {
		t.Run(row.Kind, func(t *testing.T) {
			if err := validateTelegramBackupRecord(row); err != nil {
				t.Fatalf("valid current Telegram producer record rejected: %v", err)
			}
		})
	}
}

func TestValidateTelegramBackupRecordRejectsMalformedIdentityAndShape(t *testing.T) {
	commandID := "tg_" + secure.Digest("42:17")[:40]
	tests := []struct {
		name string
		row  backupRecord
	}{
		{"command array", backupRecord{Kind: "telegram_command", ID: commandID, Data: json.RawMessage(`[]`)}},
		{"command null", backupRecord{Kind: "telegram_command", ID: commandID, Data: json.RawMessage(`null`)}},
		{"command missing identity", backupRecord{Kind: "telegram_command", ID: commandID, Data: json.RawMessage(`{"chat_id":42,"user_id":7,"message_id":17,"command":"retry","status":"done","updated_at":"2026-01-01T00:00:00Z"}`)}},
		{"command key mismatch", backupRecord{Kind: "telegram_command", ID: "tg_wrong", Data: json.RawMessage(`{"session_id":"telegram_42","chat_id":42,"user_id":7,"message_id":17,"command":"retry","status":"done","updated_at":"2026-01-01T00:00:00Z"}`)}},
		{"command wrong status", backupRecord{Kind: "telegram_command", ID: commandID, Data: json.RawMessage(`{"session_id":"telegram_42","chat_id":42,"user_id":7,"message_id":17,"command":"retry","status":"unknown","updated_at":"2026-01-01T00:00:00Z"}`)}},
		{"session chat mismatch", backupRecord{Kind: "telegram_session", ID: "chat_42", Data: json.RawMessage(`{"chat_id":7,"user_id":9,"session_id":"telegram_7","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)}},
		{"retry key mismatch", backupRecord{Kind: "telegram_retry", ID: "telegram_42:wrong", Data: json.RawMessage(`{"session_id":"telegram_42","owner":"sender-1","attempt_id":"attempt","prompt":"retry me","updated_at":"2026-01-01T00:00:00Z"}`)}},
		{"chat pending orphan", backupRecord{Kind: "telegram_chat", ID: telegramChatKey(42, 91), Data: json.RawMessage(`{"chat_id":42,"update_id":91,"message_id":9,"user_id":7,"session_id":"telegram_42","status":"pending","updated_at":"2026-01-01T00:00:00Z"}`)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateTelegramBackupRecord(tc.row); err == nil {
				t.Fatal("malformed Telegram backup record was accepted")
			}
		})
	}
}
