package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/midi-HL/muse-bridge/internal/secure"
)

type telegramSessionRecord struct {
	ChatID    int64  `json:"chat_id"`
	UserID    int64  `json:"user_id"`
	SessionID string `json:"session_id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func telegramSessionKey(chatID int64) string { return fmt.Sprintf("chat_%d", chatID) }
func telegramSessionID(chatID int64) string  { return "telegram_" + strconv.FormatInt(chatID, 10) }
func telegramChatIDFromSession(sessionID string) int64 {
	if strings.HasPrefix(sessionID, "telegram_") {
		part := strings.SplitN(strings.TrimPrefix(sessionID, "telegram_"), "_", 2)[0]
		n, _ := strconv.ParseInt(part, 10, 64)
		return n
	}
	return 0
}

// TelegramSession returns the persisted conversation selected for a chat. The
// pointer is public metadata only; account cookies and other secrets stay in
// their existing encrypted stores.
func (s *Service) TelegramSession(ctx context.Context, chatID, userID int64) (string, error) {
	s.telegramSessionMu.Lock()
	defer s.telegramSessionMu.Unlock()
	if chatID == 0 {
		return "", errors.New("telegram chat id is required")
	}
	key := telegramSessionKey(chatID)
	var rec telegramSessionRecord
	err := s.db.Get(ctx, "telegram_session", key, &rec)
	if errors.Is(err, sql.ErrNoRows) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		rec = telegramSessionRecord{ChatID: chatID, UserID: userID, SessionID: telegramSessionID(chatID), CreatedAt: now, UpdatedAt: now}
		legacyID := telegramSessionID(chatID)
		var legacy sessionRecord
		if s.db.Get(ctx, "session", legacyID, &legacy) == nil && legacy.Platform == "telegram" {
			if sender, parseErr := strconv.ParseInt(legacy.UserID, 10, 64); parseErr == nil && sender != 0 {
				rec.UserID = sender
			}
		}
		if err := s.db.Put(ctx, "telegram_session", key, rec, nil); err != nil {
			return "", err
		}
		if err := s.migrateLegacyTelegramSession(ctx, rec); err != nil {
			return "", err
		}
		return rec.SessionID, nil
	}
	if err != nil {
		return "", err
	}
	if rec.ChatID != chatID || rec.SessionID == "" {
		return "", errors.New("telegram session belongs to another user")
	}
	if err := s.migrateLegacyTelegramSession(ctx, rec); err != nil {
		return "", err
	}
	return rec.SessionID, nil
}

func (s *Service) migrateLegacyTelegramSession(ctx context.Context, pointer telegramSessionRecord) error {
	legacyID := telegramSessionID(pointer.ChatID)
	if pointer.SessionID != legacyID {
		return nil
	}
	var legacy sessionRecord
	if err := s.db.Get(ctx, "session", legacyID, &legacy); err != nil {
		return nil
	}
	if legacy.Platform != "telegram" || legacy.UserID == strconv.FormatInt(pointer.ChatID, 10) {
		return nil
	}
	if legacy.UserID != "" && legacy.UserID != strconv.FormatInt(pointer.UserID, 10) {
		return errors.New("legacy Telegram session owner mismatch")
	}
	legacy.UserID = strconv.FormatInt(pointer.ChatID, 10)
	return s.db.Put(ctx, "session", legacy.ID, legacy, nil)
}

func (s *Service) NewTelegramSession(ctx context.Context, chatID, userID int64) (string, error) {
	s.telegramSessionMu.Lock()
	defer s.telegramSessionMu.Unlock()
	if chatID == 0 {
		return "", errors.New("telegram chat id is required")
	}
	id := telegramSessionID(chatID) + "_" + strings.TrimPrefix(secure.ID(), "telegram_")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rec := telegramSessionRecord{ChatID: chatID, UserID: userID, SessionID: id, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Put(ctx, "telegram_session", telegramSessionKey(chatID), rec, nil); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) TelegramLastUserPrompt(ctx context.Context, sessionID, platform, userID string) (string, error) {
	var rec sessionRecord
	if err := s.db.Get(ctx, "session", sessionID, &rec); err != nil {
		return "", errors.New("no previous Telegram request")
	}
	if rec.Platform != "" && (rec.Platform != platform || rec.UserID != userID) {
		return "", errors.New("session belongs to another user")
	}
	for i := len(rec.Messages) - 1; i >= 0; i-- {
		if rec.Messages[i].Role == "user" && strings.TrimSpace(rec.Messages[i].Content) != "" {
			return rec.Messages[i].Content, nil
		}
	}
	return "", errors.New("no previous Telegram request")
}
