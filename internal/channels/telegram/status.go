package telegram

import "context"

// ChatStatus reports non-conversational state without adding it to model text.
type ChatStatus interface {
	ChatWithStatus(context.Context, Request, func(string) error, func(context.Context, string) error) (Response, error)
}

func (s *StreamEditor) Status(ctx context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.hasText || s.messageID == 0 || text == "" {
		return nil
	}
	return s.editCurrent(ctx, text)
}
