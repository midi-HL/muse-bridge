package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type telegramCommandRecord struct {
	SessionID string `json:"session_id"`
	ChatID    int64  `json:"chat_id"`
	UserID    int64  `json:"user_id"`
	MessageID int    `json:"message_id"`
	Command   string `json:"command"`
	Status    string `json:"status"`
	Text      string `json:"text,omitempty"`
	Delivered bool   `json:"delivered,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// TelegramCommandOnce separates generation from delivery. If Telegram retries a
// command update after sendMessage failed, reuse its stored result. A pending
// command found after process restart is never automatically regenerated.
func (s *Service) TelegramCommandOnce(ctx context.Context, session string, chatID, userID int64, messageID int, name string, fn func() (string, error)) (string, error) {
	if messageID <= 0 {
		return fn()
	}
	key := telegramCommandKey(chatID, messageID)
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

// TelegramCommandDeliver prevents concurrent or restarted copies of the same
// command update from sending a second successful reply. Failed sends remain
// eligible for a later redelivery.
func (s *Service) TelegramCommandDeliver(ctx context.Context, chatID, userID int64, messageID int, name string, send func() error) (bool, error) {
	if messageID <= 0 {
		return true, send()
	}
	key := telegramCommandKey(chatID, messageID)
	for {
		s.telegramCommandMu.Lock()
		if wait := s.telegramDeliveries[key]; wait != nil {
			s.telegramCommandMu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		var record telegramCommandRecord
		err := s.db.Get(ctx, "telegram_command", key, &record)
		if err != nil {
			s.telegramCommandMu.Unlock()
			return false, err
		}
		if record.ChatID != chatID || record.UserID != userID || record.MessageID != messageID || record.Command != name {
			s.telegramCommandMu.Unlock()
			return false, errors.New("Telegram command identity mismatch")
		}
		if record.Status == "invalidated" {
			s.telegramCommandMu.Unlock()
			return true, nil
		}
		// TelegramCommandOnce waits for a live command. An incomplete notice
		// can therefore be delivered only for a settled failure or orphaned claim.
		if s.telegramCommands[key] != nil {
			s.telegramCommandMu.Unlock()
			return true, nil
		}
		if record.Status != "done" && record.Status != "pending" && record.Status != "failed" {
			s.telegramCommandMu.Unlock()
			return false, nil
		}
		if record.Delivered {
			s.telegramCommandMu.Unlock()
			return true, nil
		}
		if s.telegramDeliveries == nil {
			s.telegramDeliveries = map[string]chan struct{}{}
		}
		finished := make(chan struct{})
		s.telegramDeliveries[key] = finished
		s.telegramCommandMu.Unlock()
		sendErr := send()
		s.telegramCommandMu.Lock()
		var latest telegramCommandRecord
		loadErr := s.db.Get(context.Background(), "telegram_command", key, &latest)
		if loadErr == nil && latest.Status == "invalidated" {
			delete(s.telegramDeliveries, key)
			close(finished)
			s.telegramCommandMu.Unlock()
			return true, nil
		}
		// A delivery marker belongs to the exact result read before sending.
		// A pending notice must never mark a later completed answer delivered.
		if sendErr == nil && loadErr == nil && latest == record {
			latest.Delivered = true
			latest.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			saveErr := s.db.Put(saveCtx, "telegram_command", key, latest, nil)
			cancel()
			delete(s.telegramDeliveries, key)
			close(finished)
			s.telegramCommandMu.Unlock()
			if saveErr != nil {
				return false, errors.New("cannot persist Telegram delivery result")
			}
			return true, nil
		}
		delete(s.telegramDeliveries, key)
		close(finished)
		s.telegramCommandMu.Unlock()
		if sendErr != nil {
			return false, sendErr
		}
		if loadErr != nil {
			return false, loadErr
		}
		return true, nil
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

func (s *Service) telegramCommandMutationPendingLocked(sessionID string) (bool, error) {
	rows, err := s.db.List(context.Background(), "telegram_command")
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		var record telegramCommandRecord
		if err := json.Unmarshal(row.Data, &record); err != nil {
			return false, errors.New("invalid Telegram command state")
		}
		if record.SessionID == sessionID && record.Command == "rename" && s.telegramCommands[row.ID] != nil {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) telegramCommandDeliveryPendingLocked(sessionID string) (bool, error) {
	rows, err := s.db.List(context.Background(), "telegram_command")
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		var rec telegramCommandRecord
		if err = json.Unmarshal(row.Data, &rec); err != nil {
			return false, errors.New("invalid Telegram command state")
		}
		if rec.SessionID == sessionID && s.telegramDeliveries[row.ID] != nil {
			return true, nil
		}
	}
	return false, nil
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
