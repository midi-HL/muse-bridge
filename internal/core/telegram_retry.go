package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/midi-HL/muse-bridge/internal/secure"
)

type telegramRetryState struct {
	SessionID string `json:"session_id"`
	Owner     string `json:"owner"`
	AttemptID string `json:"attempt_id"`
	Prompt    string `json:"prompt"`
	Committed bool   `json:"committed"`
	UpdatedAt string `json:"updated_at"`
}

func telegramRetryKey(sessionID, owner string) string {
	return sessionID + ":" + secure.Digest(owner)[:16]
}

// beginTelegramAttempt is called only after Chat owns the session lease, so a
// rejected busy request cannot replace the prior retryable input.
func (s *Service) beginTelegramAttempt(ctx context.Context, sessionID, owner, prompt string) (telegramRetryState, error) {
	prompt = strings.TrimSpace(prompt)
	if sessionID == "" || owner == "" || prompt == "" {
		return telegramRetryState{}, errors.New("Telegram retry input is invalid")
	}
	var previous telegramRetryState
	if err := s.db.Get(ctx, "telegram_retry", telegramRetryKey(sessionID, owner), &previous); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return telegramRetryState{}, err
	}
	rec := telegramRetryState{SessionID: sessionID, Owner: owner, Prompt: prompt, AttemptID: secure.ID(), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.db.Put(ctx, "telegram_retry", telegramRetryKey(sessionID, owner), rec, nil); err != nil {
		return telegramRetryState{}, err
	}
	return rec, nil
}
func (s *Service) telegramRetryRecord(ctx context.Context, sessionID, owner string) (telegramRetryState, error) {
	var rec telegramRetryState
	err := s.db.Get(ctx, "telegram_retry", telegramRetryKey(sessionID, owner), &rec)
	return rec, err
}
func (s *Service) invalidateTelegramRetries(ctx context.Context, sessionID string) error {
	rows, err := s.db.List(ctx, "telegram_retry")
	if err != nil {
		return err
	}
	for _, row := range rows {
		var rec telegramRetryState
		if json.Unmarshal(row.Data, &rec) == nil && rec.SessionID == sessionID {
			if err := s.db.Delete(ctx, "telegram_retry", row.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
	}
	return nil
}

func (s *Service) TelegramRetryAttempt(ctx context.Context, sessionID, owner string) (string, error) {
	rec, err := s.telegramRetryRecord(ctx, sessionID, owner)
	if err != nil || rec.Owner != owner || rec.AttemptID == "" || strings.TrimSpace(rec.Prompt) == "" {
		return "", errors.New("no previous Telegram request")
	}
	return rec.AttemptID, nil
}
func (s *Service) RememberTelegramPrompt(ctx context.Context, sessionID, owner, prompt string) error {
	_, err := s.beginTelegramAttempt(ctx, sessionID, owner, prompt)
	return err
}
func (s *Service) TelegramRetrySnapshot(ctx context.Context, sessionID, owner string) (string, string, error) {
	rec, err := s.telegramRetryRecord(ctx, sessionID, owner)
	if err != nil || rec.Owner != owner || strings.TrimSpace(rec.Prompt) == "" || rec.AttemptID == "" {
		return "", "", errors.New("no previous Telegram request")
	}
	return rec.AttemptID, rec.Prompt, nil
}
func (s *Service) TelegramLastPrompt(ctx context.Context, sessionID, owner string) (string, error) {
	rec, err := s.telegramRetryRecord(ctx, sessionID, owner)
	if err != nil || rec.Owner != owner || strings.TrimSpace(rec.Prompt) == "" {
		return "", errors.New("no previous Telegram request")
	}
	return rec.Prompt, nil
}
