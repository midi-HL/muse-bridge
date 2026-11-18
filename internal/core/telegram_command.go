package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/midi-HL/muse-bridge/internal/secure"
)

type telegramCommandRecord struct {
	SessionID string `json:"session_id"`
	ChatID    int64  `json:"chat_id"`
	UserID    int64  `json:"user_id"`
	MessageID int    `json:"message_id"`
	Command   string `json:"command"`
	Status    string `json:"status"`
	Text      string `json:"text,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// TelegramCommandOnce separates generation from delivery. If Telegram retries a
// command update after sendMessage failed, reuse its stored result. A pending
// command found after process restart is never automatically regenerated.
func (s *Service) TelegramCommandOnce(ctx context.Context, session string, chatID, userID int64, messageID int, name string, fn func() (string, error)) (string, error) {
	if messageID <= 0 {
		return fn()
	}
	key := "tg_" + secure.Digest(strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(messageID))[:40]
	for {
		s.telegramCommandMu.Lock()
		if wait := s.telegramCommands[key]; wait != nil {
			s.telegramCommandMu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		var record telegramCommandRecord
		err := s.db.Get(ctx, "telegram_command", key, &record)
		if err == nil {
			s.telegramCommandMu.Unlock()
			if record.ChatID != chatID || record.UserID != userID || record.MessageID != messageID || record.Command != name {
				return "", errors.New("Telegram command identity mismatch")
			}
			switch record.Status {
			case "done":
				return record.Text, nil
			case "invalidated":
				return "该会话已被重置，请发送新请求。", nil
			default:
				return "上一次命令未完成，请发送一条新的命令重试。", nil
			}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			s.telegramCommandMu.Unlock()
			return "", err
		}
		record = telegramCommandRecord{SessionID: session, ChatID: chatID, UserID: userID, MessageID: messageID, Command: name, Status: "pending", UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err = s.db.Put(ctx, "telegram_command", key, record, nil); err != nil {
			s.telegramCommandMu.Unlock()
			return "", err
		}
		if s.telegramCommands == nil {
			s.telegramCommands = map[string]chan struct{}{}
		}
		finished := make(chan struct{})
		s.telegramCommands[key] = finished
		s.telegramCommandMu.Unlock()
		text, callErr := invokeTelegramCommand(fn)
		record.Status = "done"
		record.Text = text
		if callErr != nil {
			record.Status = "failed"
			record.Text = ""
		}
		record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		// Reset holds the same mutex while writing the invalidated tombstone.
		// Keep the check and conditional result write in one critical section.
		s.telegramCommandMu.Lock()
		var latest telegramCommandRecord
		if checkErr := s.db.Get(context.Background(), "telegram_command", key, &latest); checkErr == nil && latest.Status == "invalidated" {
			delete(s.telegramCommands, key)
			close(finished)
			s.telegramCommandMu.Unlock()
			return "该会话已被重置，请发送新请求。", nil
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		saveErr := s.db.Put(saveCtx, "telegram_command", key, record, nil)
		cancel()
		delete(s.telegramCommands, key)
		close(finished)
		s.telegramCommandMu.Unlock()
		if saveErr != nil {
			return "", errors.New("cannot persist Telegram command result")
		}
		return text, callErr
	}
}
func invokeTelegramCommand(fn func() (string, error)) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = errors.New("Telegram command failed")
		}
	}()
	return fn()
}

func (s *Service) invalidateTelegramCommands(ctx context.Context, sessionID string) error {
	rows, err := s.db.List(ctx, "telegram_command")
	if err != nil {
		return err
	}
	for _, row := range rows {
		var record telegramCommandRecord
		if json.Unmarshal(row.Data, &record) != nil {
			return errors.New("invalid Telegram command state")
		}
		if record.SessionID == sessionID {
			record.Status = "invalidated"
			record.Text = ""
			if err = s.db.Put(ctx, "telegram_command", row.ID, record, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// StopTelegramChat uses the actual core leases rather than a second adapter map.
// It also cancels an older generation if /new changed the selected pointer.
func (s *Service) StopTelegramChat(chatID int64) bool {
	if chatID == 0 {
		return false
	}
	base := telegramSessionID(chatID)
	stopped := false
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	for id, cancel := range s.sessions {
		if id == base || strings.HasPrefix(id, base+"_") {
			cancel()
			stopped = true
		}
	}
	return stopped
}
