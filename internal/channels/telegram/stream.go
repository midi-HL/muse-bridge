package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const telegramTextLimit = 4096
const streamPlaceholder = "Muse 正在生成…"

type StreamEditor struct {
	client    *Client
	chatID    int64
	interval  time.Duration
	mu        sync.Mutex
	messageID int
	current   string
	rendered  string
	full      string
	last      time.Time
	hasText   bool
	closed    bool
}

func NewStreamEditor(client *Client, chatID int64, interval time.Duration) *StreamEditor {
	if interval <= 0 {
		interval = 1200 * time.Millisecond
	}
	return &StreamEditor{client: client, chatID: chatID, interval: interval}
}
func telegramUnits(s string) int { return len(utf16.Encode([]rune(s))) }

// splitTelegramChunk respects Telegram's 4096 UTF-16 unit limit and prefers a
// line or word boundary without splitting a Unicode code point.
func splitTelegramChunk(text string, max int) (string, string) {
	if max <= 0 {
		max = telegramTextLimit
	}
	r := []rune(text)
	units, cut := 0, 0
	for i, ch := range r {
		n := 1
		if ch > 0xffff {
			n = 2
		}
		if units+n > max {
			break
		}
		units += n
		cut = i + 1
	}
	if cut == len(r) {
		return text, ""
	}
	if cut == 0 {
		return "", text
	}
	for i := cut; i > 0; i-- {
		if r[i-1] == '\n' || r[i-1] == ' ' {
			cut = i
			break
		}
	}
	return string(r[:cut]), string(r[cut:])
}
func splitAllTelegramText(text string, max int) []string {
	if text == "" {
		return []string{""}
	}
	out := []string{}
	for text != "" {
		part, rest := splitTelegramChunk(text, max)
		if part == "" {
			break
		}
		out = append(out, part)
		text = rest
	}
	return out
}

func (s *StreamEditor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("telegram: stream already closed")
	}
	if s.messageID != 0 {
		return nil
	}
	id, err := s.client.Send(ctx, s.chatID, streamPlaceholder, false)
	if err != nil {
		return err
	}
	s.messageID = id
	s.current = streamPlaceholder
	s.last = time.Now()
	return nil
}
func (s *StreamEditor) editCurrent(ctx context.Context, text string) error {
	if s.messageID == 0 {
		return errors.New("telegram: stream message is unavailable")
	}
	if err := s.client.Edit(ctx, s.chatID, s.messageID, MarkdownHTML(text)); err != nil {
		return err
	}
	s.rendered = text
	s.last = time.Now()
	return nil
}
func (s *StreamEditor) sendCurrent(ctx context.Context, text string) error {
	id, err := s.client.Send(ctx, s.chatID, MarkdownHTML(text), true)
	if err != nil {
		return err
	}
	s.messageID = id
	s.rendered = text
	s.last = time.Now()
	return nil
}

func (s *StreamEditor) Push(ctx context.Context, delta string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("telegram: stream already closed")
	}
	if s.messageID == 0 {
		id, err := s.client.Send(ctx, s.chatID, streamPlaceholder, false)
		if err != nil {
			return err
		}
		s.messageID = id
		s.current = streamPlaceholder
		s.last = time.Now()
	}
	s.full += delta
	if !s.hasText && strings.TrimSpace(s.current) == streamPlaceholder {
		s.current = ""
	}
	s.current += delta
	if strings.TrimSpace(s.current) == "" {
		return nil
	}
	if telegramUnits(s.current) > telegramTextLimit {
		prefix, rest := splitTelegramChunk(s.current, telegramTextLimit)
		if prefix == "" {
			return errors.New("telegram: unable to split oversized message")
		}
		if !s.hasText {
			if err := s.editCurrent(ctx, prefix); err != nil {
				return err
			}
		} else if err := s.editCurrent(ctx, prefix); err != nil {
			return err
		}
		s.hasText = true
		s.messageID = 0
		s.current = ""
		for telegramUnits(rest) > telegramTextLimit {
			part, tail := splitTelegramChunk(rest, telegramTextLimit)
			if part == "" {
				return errors.New("telegram: unable to split oversized message")
			}
			if err := s.sendCurrent(ctx, part); err != nil {
				return err
			}
			s.messageID = 0
			s.current = ""
			rest = tail
		}
		if rest != "" {
			if err := s.sendCurrent(ctx, rest); err != nil {
				return err
			}
			s.hasText = true
		}
		return nil
	}
	if !s.hasText {
		if err := s.editCurrent(ctx, s.current); err != nil {
			return err
		}
		s.hasText = true
		return nil
	}
	if time.Since(s.last) >= s.interval {
		return s.editCurrent(ctx, s.current)
	}
	return nil
}

func (s *StreamEditor) Close(ctx context.Context, final string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.full == "" && final != "" {
		s.full = final
		s.current = final
	}
	if strings.TrimSpace(s.full) == "" {
		if s.messageID != 0 {
			return s.client.Delete(ctx, s.chatID, s.messageID)
		}
		return nil
	}
	if !s.hasText {
		s.current = s.full
	}
	remaining := s.current
	if remaining == streamPlaceholder {
		remaining = s.full
	}
	for telegramUnits(remaining) > telegramTextLimit {
		part, tail := splitTelegramChunk(remaining, telegramTextLimit)
		if part == "" {
			return errors.New("telegram: unable to split oversized message")
		}
		if !s.hasText && s.messageID != 0 {
			if err := s.editCurrent(ctx, part); err != nil {
				return err
			}
		} else {
			if err := s.sendCurrent(ctx, part); err != nil {
				return err
			}
		}
		s.messageID = 0
		s.current = ""
		s.hasText = true
		remaining = tail
	}
	if strings.TrimSpace(remaining) == "" {
		if s.messageID != 0 {
			return s.client.Delete(ctx, s.chatID, s.messageID)
		}
		return nil
	}
	if s.messageID == 0 {
		return s.sendCurrent(ctx, remaining)
	}
	if s.rendered == remaining {
		return nil
	}
	return s.editCurrent(ctx, remaining)
}
func (s *StreamEditor) Abort(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if !s.hasText && s.messageID != 0 {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.client.Delete(cleanupCtx, s.chatID, s.messageID)
	}
	return nil
}
func (s *StreamEditor) Content() string { s.mu.Lock(); defer s.mu.Unlock(); return s.full }
