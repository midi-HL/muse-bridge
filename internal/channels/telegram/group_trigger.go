package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

type botIdentity struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
}

type messageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// Keep discovery scoped to the Client so replacing the token also replaces
// its identity. Concurrent updates share one lookup.
type botIdentityCache struct {
	mu         sync.Mutex
	bot        botIdentity
	validUntil time.Time
	pending    chan struct{}
	retryAfter time.Time
}

func (c *Client) getBotIdentity(ctx context.Context) botIdentity {
	cache := &c.identity
	for {
		cache.mu.Lock()
		now := time.Now()
		if now.Before(cache.validUntil) {
			bot := cache.bot
			cache.mu.Unlock()
			return bot
		}
		if now.Before(cache.retryAfter) || ctx.Err() != nil {
			cache.mu.Unlock()
			return botIdentity{}
		}
		if pending := cache.pending; pending != nil {
			cache.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return botIdentity{}
			}
		}
		pending := make(chan struct{})
		cache.pending = pending
		cache.mu.Unlock()

		lookup, cancel := context.WithTimeout(ctx, 3*time.Second)
		raw, err := c.call(lookup, "getMe", nil)
		cancel()
		var bot botIdentity
		ok := err == nil && json.Unmarshal(raw, &bot) == nil && bot.IsBot && bot.ID > 0 && bot.Username != ""
		cache.mu.Lock()
		if ok {
			cache.bot = bot
			cache.validUntil = time.Now().Add(5 * time.Minute)
		} else {
			bot = botIdentity{}
			if ctx.Err() == nil {
				cache.retryAfter = time.Now().Add(5 * time.Second)
			}
		}
		cache.pending = nil
		close(pending)
		cache.mu.Unlock()
		return bot
	}
}

func groupTriggered(ctx context.Context, u update, client *Client) bool {
	mentions := mentionedUsernames(u.Message.Text, u.Message.Entities)
	reply := u.Message.Reply
	repliesToBot := reply != nil && reply.From.IsBot && reply.From.ID > 0
	if client == nil || (!repliesToBot && len(mentions) == 0) {
		return false
	}
	bot := client.getBotIdentity(ctx)
	return (repliesToBot && reply.From.ID == bot.ID) || containsBotMention(mentions, bot.Username)
}

func containsBotMention(mentions []string, username string) bool {
	if username == "" {
		return false
	}
	for _, mention := range mentions {
		if strings.EqualFold(mention, username) {
			return true
		}
	}
	return false
}

func mentionedUsernames(text string, entities []messageEntity) []string {
	var textUTF16 []uint16
	var mentions []string
	for _, entity := range entities {
		if entity.Type != "mention" || entity.Offset < 0 || entity.Length < 2 {
			continue
		}
		if textUTF16 == nil {
			// Telegram entity coordinates count UTF-16 code units.
			textUTF16 = utf16.Encode([]rune(text))
		}
		if entity.Offset >= len(textUTF16) || entity.Length > len(textUTF16)-entity.Offset {
			continue
		}
		end := entity.Offset + entity.Length
		if textUTF16[entity.Offset] != '@' || (end < len(textUTF16) && usernameChar(textUTF16[end])) || (entity.Offset > 0 && usernameChar(textUTF16[entity.Offset-1])) {
			continue
		}
		name := textUTF16[entity.Offset+1 : end]
		valid := true
		for _, c := range name {
			if !usernameChar(c) {
				valid = false
				break
			}
		}
		if valid {
			mentions = append(mentions, string(utf16.Decode(name)))
		}
	}
	return mentions
}

func usernameChar(c uint16) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}
