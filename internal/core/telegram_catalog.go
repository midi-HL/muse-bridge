package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/midi-HL/muse-bridge/internal/hatch"
)

var (
	ErrTelegramSessionUnavailable = errors.New("Telegram session unavailable")
	ErrTelegramSessionCreator     = errors.New("only the Telegram session creator can rename it")
	ErrTelegramSessionBusy        = errors.New("Telegram session is generating")
	ErrTelegramSessionTitle       = errors.New("Telegram session title must contain 1 to 80 visible characters on one line")
)

// TelegramSessionSummary contains only metadata for a conversation in the chat.
type TelegramSessionSummary struct {
	ID, Title string
	Current   bool
}

func (s *Service) loadTelegramPointer(ctx context.Context, chatID int64) (telegramSessionRecord, error) {
	var pointer telegramSessionRecord
	if err := s.db.Get(ctx, "telegram_session", telegramSessionKey(chatID), &pointer); err != nil {
		return pointer, err
	}
	if pointer.ChatID != chatID || !ownedTelegramSessionID(pointer.SessionID, chatID) {
		return pointer, ErrTelegramSessionUnavailable
	}
	return pointer, nil
}

func ownedTelegramSessionID(id string, chatID int64) bool {
	return managedID.MatchString(id) && validTelegramSessionForChat(id, chatID)
}

func sessionOwnedByTelegramChat(record sessionRecord, id string, chatID int64) bool {
	return record.ID == id && record.Platform == "telegram" && record.UserID == strconv.FormatInt(chatID, 10)
}

// telegramCatalogSession distinguishes known empty sessions from missing or
// foreign records. Callers hold telegramSessionMu and use the same pointer
// snapshot for access checks and updates.
func (s *Service) telegramCatalogSession(ctx context.Context, pointer telegramSessionRecord, id string) (sessionRecord, error) {
	var record sessionRecord
	if !ownedTelegramSessionID(id, pointer.ChatID) {
		return record, ErrTelegramSessionUnavailable
	}
	err := s.db.Get(ctx, "session", id, &record)
	if errors.Is(err, sql.ErrNoRows) {
		if id == pointer.SessionID {
			return sessionRecord{ID: id, CreatedAt: pointer.CreatedAt}, nil
		}
		if owner, ok := pointer.Known[id]; ok {
			return sessionRecord{ID: id, CreatedAt: owner.CreatedAt}, nil
		}
		return record, ErrTelegramSessionUnavailable
	}
	if err != nil {
		return record, err
	}
	if !sessionOwnedByTelegramChat(record, id, pointer.ChatID) {
		return sessionRecord{}, ErrTelegramSessionUnavailable
	}
	return record, nil
}

// ListTelegramSessions includes the current session, saved empty sessions and
// compatible older transcripts. The current session sorts first.
func (s *Service) ListTelegramSessions(ctx context.Context, chatID int64) ([]TelegramSessionSummary, error) {
	s.telegramSessionMu.Lock()
	defer s.telegramSessionMu.Unlock()
	pointer, err := s.loadTelegramPointer(ctx, chatID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.ListPrefix(ctx, "session", telegramSessionID(chatID))
	if err != nil {
		return nil, err
	}
	type entry struct {
		TelegramSessionSummary
		updated time.Time
	}
	entries := make(map[string]entry, len(pointer.Known)+1)
	add := func(id, title, date string) {
		updated, _ := time.Parse(time.RFC3339Nano, date)
		entries[id] = entry{TelegramSessionSummary: TelegramSessionSummary{ID: id, Title: title, Current: id == pointer.SessionID}, updated: updated}
	}
	for id, owner := range pointer.Known {
		if ownedTelegramSessionID(id, chatID) {
			add(id, "", owner.CreatedAt)
		}
	}
	add(pointer.SessionID, "", pointer.CreatedAt)
	for _, row := range rows {
		if !ownedTelegramSessionID(row.ID, chatID) {
			continue
		}
		var record sessionRecord
		if json.Unmarshal(row.Data, &record) != nil || !sessionOwnedByTelegramChat(record, row.ID, chatID) {
			delete(entries, row.ID)
			continue
		}
		add(row.ID, record.Title, row.UpdatedAt)
	}
	ordered := make([]entry, 0, len(entries))
	for _, item := range entries {
		ordered = append(ordered, item)
	}
	slices.SortFunc(ordered, func(a, b entry) int {
		if a.Current != b.Current {
			if a.Current {
				return -1
			}
			return 1
		}
		if order := b.updated.Compare(a.updated); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	result := make([]TelegramSessionSummary, 0, len(ordered))
	for _, item := range ordered {
		result = append(result, item.TelegramSessionSummary)
	}
	return result, nil
}

// SwitchTelegramSession atomically saves the previous creator and selects the
// target. A legacy session with no saved creator stays unclaimed.
func (s *Service) SwitchTelegramSession(ctx context.Context, chatID int64, id string) error {
	s.telegramSessionMu.Lock()
	defer s.telegramSessionMu.Unlock()
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	pointer, err := s.loadTelegramPointer(ctx, chatID)
	if err != nil {
		return err
	}
	record, err := s.telegramCatalogSession(ctx, pointer, id)
	if err != nil {
		return err
	}
	if id == pointer.SessionID {
		return nil
	}
	pointer.rememberCurrent()
	owner := pointer.Known[id]
	pointer.SessionID, pointer.UserID = id, owner.UserID
	pointer.CreatedAt = owner.CreatedAt
	if pointer.CreatedAt == "" {
		pointer.CreatedAt = record.CreatedAt
	}
	pointer.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.db.Put(ctx, "telegram_session", telegramSessionKey(chatID), pointer, nil)
}

// RenameTelegramSession updates the conversation bound to the command. It
// rejects stale selection, a different creator, and an active chat lease.
func (s *Service) RenameTelegramSession(ctx context.Context, chatID, userID int64, id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > 80 || strings.ContainsFunc(title, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
	}) {
		return ErrTelegramSessionTitle
	}
	s.telegramSessionMu.Lock()
	defer s.telegramSessionMu.Unlock()
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	pointer, err := s.loadTelegramPointer(ctx, chatID)
	if err != nil {
		return err
	}
	if pointer.SessionID != id {
		return ErrTelegramSessionUnavailable
	}
	if userID == 0 || pointer.UserID != userID {
		return ErrTelegramSessionCreator
	}
	if s.sessions[id] != nil {
		return ErrTelegramSessionBusy
	}
	record, err := s.telegramCatalogSession(ctx, pointer, id)
	if err != nil {
		return err
	}
	if record.Platform == "" {
		record.Platform, record.UserID = "telegram", strconv.FormatInt(chatID, 10)
	}
	record.Title = title
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return s.db.Put(ctx, "session", id, record, nil)
}

// TelegramSessionHistory returns only recent conversational messages.
func (s *Service) TelegramSessionHistory(ctx context.Context, chatID int64, id string) ([]hatch.Message, error) {
	s.telegramSessionMu.Lock()
	defer s.telegramSessionMu.Unlock()
	pointer, err := s.loadTelegramPointer(ctx, chatID)
	if err != nil {
		return nil, err
	}
	record, err := s.telegramCatalogSession(ctx, pointer, id)
	if err != nil {
		return nil, err
	}
	messages := make([]hatch.Message, 0, 10)
	for i := len(record.Messages) - 1; i >= 0 && len(messages) < cap(messages); i-- {
		message := record.Messages[i]
		if message.Role == "user" || message.Role == "assistant" {
			messages = append(messages, message)
		}
	}
	slices.Reverse(messages)
	return messages, nil
}
