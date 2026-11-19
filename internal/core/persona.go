package core

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func (s *Service) personaPrompt(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	if !managedID.MatchString(id) {
		return "", errors.New("invalid persona identifier")
	}
	var preset personaRecord
	if err := s.db.Get(ctx, "persona", id, &preset); err != nil {
		return "", errors.New("persona not found")
	}
	if !preset.Enabled {
		return "", errors.New("persona is disabled")
	}
	return preset.Prompt, nil
}

// Resolve a new default once. Continuing sessions retain their explicit choice,
// including an explicit empty choice. The adapter receives prompt text, never ID.
func (s *Service) resolvePersona(ctx context.Context, requested string, history sessionRecord) (string, string, error) {
	id := requested
	if id == "_none" {
		id = ""
	} else if id == "" {
		if history.PersonaSelected {
			id = history.PersonaID
		} else {
			id, _ = s.Settings()["persona.default_id"].(string)
		}
	}
	prompt, err := s.personaPrompt(ctx, id)
	return id, prompt, err
}

// SetSessionPersona is shared by authenticated admin actions and the Telegram
// adapter. Non-admin callers supply platform/user identity; they cannot mutate
// an existing session owned by another user or platform.
func (s *Service) SetSessionPersona(ctx context.Context, id, platform, userID, personaID string) (any, error) {
	if id == "" || !managedID.MatchString(id) {
		return nil, errors.New("invalid session identifier")
	}
	if personaID == "_none" {
		personaID = ""
	}
	if _, err := s.personaPrompt(ctx, personaID); err != nil {
		return nil, err
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if s.sessions[id] != nil {
		return nil, errors.New("session is generating; stop it before switching persona")
	}
	var record sessionRecord
	err := s.db.Get(ctx, "session", id, &record)
	if errors.Is(err, sql.ErrNoRows) {
		if platform == "" {
			return nil, errors.New("session not found")
		}
		record = sessionRecord{ID: id, Platform: platform, UserID: userID, Messages: []hatch.Message{}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	} else if err != nil {
		return nil, err
	}
	if platform != "" && (record.Platform != platform || record.UserID != userID) {
		return nil, errors.New("session belongs to another user")
	}
	record.PersonaID, record.PersonaSelected = personaID, true
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return map[string]any{"id": id, "persona_id": personaID, "persona_selected": true}, s.db.Put(ctx, "session", id, record, nil)
}

func (s *Service) TelegramPersonas(ctx context.Context, sessionID, userID, selection string) (string, error) {
	chatID := telegramChatIDFromSession(sessionID)
	if chatID == 0 {
		return "", errors.New("invalid Telegram session")
	}
	s.telegramSessionMu.Lock()
	defer s.telegramSessionMu.Unlock()
	var pointer telegramSessionRecord
	if err := s.db.Get(ctx, "telegram_session", telegramSessionKey(chatID), &pointer); err != nil || pointer.SessionID != sessionID {
		return "", errors.New("Telegram session is unavailable")
	}
	selection = strings.TrimSpace(selection)
	if selection != "" {
		if pointer.UserID == 0 || strconv.FormatInt(pointer.UserID, 10) != userID {
			return "人设切换失败：只有创建该会话的用户可以切换人设。", nil
		}
		id := selection
		if id == "none" {
			id = "_none"
		}
		_, err := s.SetSessionPersona(ctx, sessionID, "telegram", strconv.FormatInt(chatID, 10), id)
		if err != nil {
			return "人设切换失败：请确认 ID 有效、预设已启用，并停止当前生成。", nil
		}
		return "人设已切换。人设仅作引导，无法移除 Muse 自带系统提示词。", nil
	}
	rows, err := s.db.List(ctx, "persona")
	if err != nil {
		return "", err
	}
	var text strings.Builder
	text.WriteString("人设预设（/persona ID 切换，/persona none 关闭）：\n")
	for _, row := range rows {
		var p personaRecord
		if s.db.Get(ctx, "persona", row.ID, &p) == nil && p.Enabled {
			text.WriteString(p.ID + " · " + p.Name + "\n")
		}
	}
	text.WriteString("人设仅作引导，无法移除 Muse 自带系统提示词。")
	return text.String(), nil
}
