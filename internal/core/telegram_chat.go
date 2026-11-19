package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type telegramChatRecord struct {
	ChatID    int64  `json:"chat_id"`
	UpdateID  int64  `json:"update_id"`
	MessageID int    `json:"message_id"`
	UserID    int64  `json:"user_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Text      string `json:"text,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

func telegramChatKey(chatID, updateID int64) string {
	return fmt.Sprintf("tgchat_%d_%d", chatID, updateID)
}

func (s *Service) telegramChatPendingLocked(session string) (bool, error) {
	rows, err := s.db.List(context.Background(), "telegram_chat")
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		var record telegramChatRecord
		if err := json.Unmarshal(row.Data, &record); err != nil {
			return false, errors.New("invalid Telegram chat update state")
		}
		if record.SessionID == session && record.Status == "pending" {
			return true, nil
		}
	}
	return false, nil
}

// TelegramMessageOnce deduplicates webhook and polling redeliveries of an ordinary chat update.
func (s *Service) TelegramMessageOnce(ctx context.Context, chatID, updateID int64, messageID int, userID int64, session string, fn func() (string, error)) (string, error) {
	if updateID <= 0 {
		return fn()
	}
	key := telegramChatKey(chatID, updateID)
	s.sessionsMu.Lock()
	s.telegramCommandMu.Lock()
	unlockClaim := func() { s.telegramCommandMu.Unlock(); s.sessionsMu.Unlock() }
	var pointer telegramSessionRecord
	if err := s.db.Get(ctx, "telegram_session", telegramSessionKey(chatID), &pointer); err != nil || pointer.SessionID != session {
		unlockClaim()
		return "该会话已被重置或切换，请发送一条新消息。", nil
	}
	if s.telegramCommands[key] != nil {
		unlockClaim()
		return "", errors.New("Telegram update is already processing")
	}
	var record telegramChatRecord
	err := s.db.Get(ctx, "telegram_chat", key, &record)
	if err == nil {
		unlockClaim()
		if record.ChatID != chatID || record.UpdateID != updateID || record.MessageID != messageID || record.UserID != userID {
			return "", errors.New("Telegram update identity mismatch")
		}
		switch record.Status {
		case "done":
			return record.Text, nil
		case "pending", "failed":
			return "该消息已处理或处理失败，请发送一条新消息。", nil
		case "invalidated":
			return "该会话已被重置，请发送一条新消息。", nil
		default:
			return "", errors.New("invalid Telegram chat update status")
		}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		unlockClaim()
		return "", err
	}
	record = telegramChatRecord{ChatID: chatID, UpdateID: updateID, MessageID: messageID, UserID: userID, SessionID: session, Status: "pending", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = s.db.Put(ctx, "telegram_chat", key, record, nil); err != nil {
		unlockClaim()
		return "", err
	}
	if s.telegramCommands == nil {
		s.telegramCommands = map[string]chan struct{}{}
	}
	finished := make(chan struct{})
	s.telegramCommands[key] = finished
	unlockClaim()
	text, callErr := fn()
	if callErr == nil {
		record.Status = "done"
		record.Text = text
	} else {
		record.Status = "failed"
	}
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.telegramCommandMu.Lock()
	var latest telegramChatRecord
	if loadErr := s.db.Get(context.Background(), "telegram_chat", key, &latest); loadErr == nil && latest.Status == "invalidated" {
		record = latest
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	saveErr := s.db.Put(saveCtx, "telegram_chat", key, record, nil)
	cancel()
	delete(s.telegramCommands, key)
	close(finished)
	s.telegramCommandMu.Unlock()
	if saveErr != nil {
		return "", errors.New("cannot persist Telegram update result")
	}
	if record.Status == "invalidated" {
		return "该会话已被重置，请发送一条新消息。", nil
	}
	return text, callErr
}

func (s *Service) invalidateTelegramChats(ctx context.Context, session string) error {
	rows, err := s.db.List(ctx, "telegram_chat")
	if err != nil {
		return err
	}
	for _, row := range rows {
		var rec telegramChatRecord
		if err := s.db.Get(ctx, "telegram_chat", row.ID, &rec); err != nil {
			return err
		}
		if rec.SessionID != session {
			continue
		}
		rec.Status = "invalidated"
		rec.Text = ""
		rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.db.Put(ctx, "telegram_chat", row.ID, rec, nil); err != nil {
			return err
		}
	}
	return nil
}
