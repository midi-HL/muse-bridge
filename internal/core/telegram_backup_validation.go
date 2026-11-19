package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/midi-HL/muse-bridge/internal/secure"
)

// validateTelegramBackupRecord checks Telegram records at the backup boundary,
// before any destination record is changed. It deliberately tolerates unknown
// fields and historically optional metadata while validating fields that define
// ownership, identity, and the store key.
func validateTelegramBackupRecord(row backupRecord) error {
	if row.Kind != "telegram_session" && row.Kind != "telegram_retry" && row.Kind != "telegram_command" {
		return nil
	}
	fields, err := decodeTelegramBackupObject(row.Data)
	if err != nil {
		return err
	}

	switch row.Kind {
	case "telegram_session":
		if err := requireTelegramBackupFields(fields, "chat_id", "user_id", "session_id"); err != nil {
			return err
		}
		var rec telegramSessionRecord
		if err := json.Unmarshal(row.Data, &rec); err != nil {
			return fmt.Errorf("invalid Telegram session record: %w", err)
		}
		// Older and current updates without a sender can store user_id=0.
		if !validTelegramSessionForChat(rec.SessionID, rec.ChatID) || row.ID != telegramSessionKey(rec.ChatID) {
			return errors.New("invalid Telegram session identity")
		}
		for id := range rec.Known {
			if !ownedTelegramSessionID(id, rec.ChatID) {
				return errors.New("invalid Telegram session catalog identity")
			}
		}
	case "telegram_retry":
		if err := requireTelegramBackupFields(fields, "session_id", "owner", "attempt_id", "prompt"); err != nil {
			return err
		}
		var rec telegramRetryState
		if err := json.Unmarshal(row.Data, &rec); err != nil {
			return fmt.Errorf("invalid Telegram retry record: %w", err)
		}
		if strings.TrimSpace(rec.SessionID) == "" || strings.TrimSpace(rec.Owner) == "" || strings.TrimSpace(rec.AttemptID) == "" || strings.TrimSpace(rec.Prompt) == "" || row.ID != telegramRetryKey(rec.SessionID, rec.Owner) {
			return errors.New("invalid Telegram retry identity")
		}
	case "telegram_command":
		if err := requireTelegramBackupFields(fields, "session_id", "chat_id", "user_id", "message_id", "command", "status"); err != nil {
			return err
		}
		var rec telegramCommandRecord
		if err := json.Unmarshal(row.Data, &rec); err != nil {
			return fmt.Errorf("invalid Telegram command record: %w", err)
		}
		if rec.MessageID <= 0 || strings.TrimSpace(rec.Command) == "" || !validTelegramSessionForChat(rec.SessionID, rec.ChatID) {
			return errors.New("invalid Telegram command identity")
		}
		if row.ID != telegramCommandKey(rec.ChatID, rec.MessageID) {
			return errors.New("Telegram command key does not match its identity")
		}
		switch rec.Status {
		case "pending", "done", "failed", "invalidated":
		default:
			return errors.New("invalid Telegram command status")
		}
	}
	return nil
}

func decodeTelegramBackupObject(data []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("Telegram backup data must be an object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, errors.New("Telegram backup data must be an object")
	}
	return object, nil
}

func requireTelegramBackupFields(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		raw, ok := fields[name]
		if !ok || len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("Telegram backup record is missing %s", name)
		}
	}
	return nil
}

func validTelegramSessionForChat(sessionID string, chatID int64) bool {
	if chatID == 0 {
		return false
	}
	base := telegramSessionID(chatID)
	if sessionID == base {
		return true
	}
	if !strings.HasPrefix(sessionID, base+"_") {
		return false
	}
	suffix := strings.TrimPrefix(sessionID, base+"_")
	return suffix != "" && strings.TrimSpace(suffix) == suffix
}

func telegramCommandKey(chatID int64, messageID int) string {
	return "tg_" + secure.Digest(strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(messageID))[:40]
}
