// Package telegram implements the Bot API boundary without coupling it to Muse.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/midi-HL/muse-bridge/internal/media"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrUnsupported = errors.New("telegram: operation unsupported by upstream protocol")
var ErrBusy = errors.New("telegram: chat is busy")
var ErrNotConfigured = errors.New("telegram: bot token is not configured")

type Request struct {
	ChatID                         int64
	UserID                         int64
	ChatType, Username, Name, Text string
	MessageID, ThreadID            int
	SessionID                      string
	Attachments                    []media.Attachment
}
type Response struct {
	Text        string
	Attachments []media.Attachment
}
type Command struct{ Name, Args, Target string }
type Delivery struct {
	Kind, Text string
	MessageID  int
	ChatID     int64
}
type StickerAsset struct {
	ID, Emoji, FileID, MIME string
	Pack                    string
	FilePath                string
	Data                    []byte
	Description             string
}

type StickerImportFailure struct {
	ID    string `json:"id,omitempty"`
	Error string `json:"error"`
}

type StickerImportReport struct {
	Pack     string                 `json:"pack"`
	Assets   []StickerAsset         `json:"assets"`
	Total    int                    `json:"total"`
	Imported int                    `json:"imported"`
	Failed   []StickerImportFailure `json:"failed,omitempty"`
}

// SessionBinder lets stateful adapters bind an update to the session selected
// when the update was accepted, before any network side effect or async delay.
type SessionBinder interface {
	BindSession(context.Context, Request) (Request, error)
}

// CommandDelivery coordinates a persisted command result with external reply
// delivery so redelivered updates do not send duplicate successful messages.
type CommandDelivery interface {
	DeliverCommand(context.Context, Request, Command, func() error) (bool, error)
}

type Handler interface {
	Chat(context.Context, Request, func(string) error) (Response, error)
	Command(context.Context, Request, Command) (Response, error)
	Sticker(context.Context, string) (StickerAsset, error)
	Feedback(context.Context, Request, Delivery) error
}
type Callbacks struct {
	OnChat     func(context.Context, Request, func(string) error) (Response, error)
	OnCommand  func(context.Context, Request, Command) (Response, error)
	OnSticker  func(context.Context, string) (StickerAsset, error)
	OnFeedback func(context.Context, Request, Delivery) error
}

func (c Callbacks) Chat(ctx context.Context, r Request, e func(string) error) (Response, error) {
	if c.OnChat == nil {
		return Response{}, ErrUnsupported
	}
	return c.OnChat(ctx, r, e)
}
func (c Callbacks) Command(ctx context.Context, r Request, x Command) (Response, error) {
	if c.OnCommand == nil {
		return Response{}, ErrUnsupported
	}
	return c.OnCommand(ctx, r, x)
}
func (c Callbacks) Sticker(ctx context.Context, id string) (StickerAsset, error) {
	if c.OnSticker == nil {
		return StickerAsset{}, ErrUnsupported
	}
	return c.OnSticker(ctx, id)
}
func (c Callbacks) Feedback(ctx context.Context, r Request, d Delivery) error {
	if c.OnFeedback == nil {
		return nil
	}
	return c.OnFeedback(ctx, r, d)
}

type Client struct {
	token, base string
	http        *http.Client
	identity    botIdentityCache
}

func NewClient(token string, h *http.Client) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrNotConfigured
	}
	if h == nil {
		h = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{token: token, base: "https://api.telegram.org/bot" + token, http: h}, nil
}

func stickerSetName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\x00\r\n") {
		return "", errors.New("telegram: sticker set URL is invalid")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("telegram: sticker set URL is invalid")
	}
	host := strings.ToLower(u.Hostname())
	if u.Port() != "" || (host != "t.me" && host != "telegram.me") {
		return "", errors.New("telegram: sticker set URL is invalid")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "addstickers" || parts[1] == "" || len(parts[1]) > 128 {
		return "", errors.New("telegram: sticker set URL is invalid")
	}
	for _, r := range parts[1] {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return "", errors.New("telegram: sticker set URL is invalid")
		}
	}
	return parts[1], nil
}

// ImportStickerSet retrieves sticker metadata using a background context. It is
// the compact helper for callers that only need successfully parsed assets.
func ImportStickerSet(token, rawURL string, h *http.Client) ([]StickerAsset, error) {
	report, err := ImportStickerSetContext(context.Background(), token, rawURL, h)
	if err != nil {
		return nil, err
	}
	return report.Assets, nil
}

// ImportStickerSetContext retrieves metadata and preserves per-item failures for
// callers such as the core action that need an import report. It does not fetch
// sticker bytes, so no local file path is created or trusted from the response.
func ImportStickerSetContext(ctx context.Context, token, rawURL string, h *http.Client) (StickerImportReport, error) {
	name, err := stickerSetName(rawURL)
	if err != nil {
		return StickerImportReport{}, err
	}
	client, err := NewClient(token, h)
	if err != nil {
		return StickerImportReport{}, err
	}
	result, err := client.call(ctx, "getStickerSet", url.Values{"name": {name}})
	if err != nil {
		return StickerImportReport{}, err
	}
	var set struct {
		Name     string `json:"name"`
		Stickers []struct {
			ID       string `json:"file_id"`
			UniqueID string `json:"file_unique_id"`
			Emoji    string `json:"emoji"`
			IsAnim   bool   `json:"is_animated"`
			IsVideo  bool   `json:"is_video"`
		} `json:"stickers"`
	}
	if err := json.Unmarshal(result, &set); err != nil {
		return StickerImportReport{}, errors.New("telegram: invalid sticker set response")
	}
	if set.Name == "" {
		set.Name = name
	}
	report := StickerImportReport{Pack: set.Name, Total: len(set.Stickers), Assets: make([]StickerAsset, 0, len(set.Stickers))}
	for _, sticker := range set.Stickers {
		if sticker.ID == "" {
			report.Failed = append(report.Failed, StickerImportFailure{ID: sticker.UniqueID, Error: "missing sticker file_id"})
			continue
		}
		mime := "image/webp"
		if sticker.IsAnim {
			mime = "application/x-tgsticker"
		} else if sticker.IsVideo {
			mime = "video/webm"
		}
		id := sticker.UniqueID
		if id == "" {
			id = sticker.ID
		}
		asset := StickerAsset{ID: id, Emoji: sticker.Emoji, FileID: sticker.ID, MIME: mime, Pack: set.Name}
		// A file_id is a safe opaque Telegram identifier; no local path is derived.
		report.Assets = append(report.Assets, asset)
		report.Imported++
	}
	return report, nil
}
func (c *Client) call(ctx context.Context, method string, body url.Values) (json.RawMessage, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, strings.NewReader(body.Encode()))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if e != nil {
		return nil, e
	}
	var out struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if json.Unmarshal(raw, &out) != nil || !out.OK {
		return nil, errors.New("telegram: API request failed")
	}
	return out.Result, nil
}
func (c *Client) Send(ctx context.Context, chat int64, text string, htmlMode bool) (int, error) {
	chunks := splitAllTelegramText(text, telegramTextLimit)
	last := 0
	for _, part := range chunks {
		v := url.Values{"chat_id": {strconv.FormatInt(chat, 10)}, "text": {part}}
		if htmlMode {
			v.Set("parse_mode", "HTML")
		}
		raw, e := c.call(ctx, "sendMessage", v)
		if e != nil {
			return 0, e
		}
		var m struct {
			MessageID int `json:"message_id"`
		}
		_ = json.Unmarshal(raw, &m)
		last = m.MessageID
	}
	return last, nil
}
func (c *Client) Edit(ctx context.Context, chat int64, id int, text string) error {
	_, e := c.call(ctx, "editMessageText", url.Values{"chat_id": {strconv.FormatInt(chat, 10)}, "message_id": {strconv.Itoa(id)}, "text": {text}})
	return e
}

func (c *Client) Delete(ctx context.Context, chat int64, id int) error {
	_, err := c.call(ctx, "deleteMessage", url.Values{"chat_id": {strconv.FormatInt(chat, 10)}, "message_id": {strconv.Itoa(id)}})
	return err
}

// SetWebhook registers Telegram's HTTPS callback. The secret is sent only in
// Telegram's API request and is never included in errors or logs.
func (c *Client) SetWebhook(ctx context.Context, endpoint, secret string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("telegram: webhook URL must be an HTTPS URL")
	}
	values := url.Values{"url": {endpoint}}
	if secret != "" {
		values.Set("secret_token", secret)
	}
	_, err = c.call(ctx, "setWebhook", values)
	return err
}

func (c *Client) DeleteWebhook(ctx context.Context) error {
	_, err := c.call(ctx, "deleteWebhook", url.Values{})
	return err
}
func Split(s string, max int) []string {
	if max < 1 {
		max = 4096
	}
	r := []rune(s)
	out := []string{}
	for len(r) > max {
		cut := max
		for i := max; i > 0; i-- {
			if r[i-1] == '\n' || r[i-1] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}
func MarkdownHTML(s string) string { return html.EscapeString(s) }

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		MessageID int `json:"message_id"`
		From      struct {
			ID        int64  `json:"id"`
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
		} `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		SenderChat *struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"sender_chat"`
		Text     string          `json:"text"`
		Entities []messageEntity `json:"entities"`
		Reply    *struct {
			From botIdentity `json:"from"`
		} `json:"reply_to_message"`
	} `json:"message"`
}
type Channel struct {
	client      *Client
	handler     Handler
	whitelist   map[int64]bool
	mode        string
	botUsername string
	poll        time.Duration
	mu          sync.Mutex
}

func New(cfg map[string]any, h Handler) (*Channel, error) {
	token, _ := cfg["telegram.token"].(string)
	c, e := NewClient(token, nil)
	if e != nil {
		return nil, e
	}
	ch := &Channel{client: c, handler: h, mode: "polling", poll: 1 * time.Second, whitelist: map[int64]bool{}}
	if name, ok := cfg["telegram.bot_username"].(string); ok {
		ch.botUsername = strings.TrimPrefix(strings.TrimSpace(name), "@")
	}
	if m, ok := cfg["telegram.mode"].(string); ok {
		ch.mode = m
	}
	if raw, ok := cfg["telegram.whitelist"].(string); ok {
		for _, v := range strings.Split(raw, ",") {
			if id, e := strconv.ParseInt(strings.TrimSpace(v), 10, 64); e == nil {
				ch.whitelist[id] = true
			}
		}
	}
	return ch, nil
}

// parseTelegramCommand also accepts group commands addressed to this bot.
func parseTelegramCommand(text string) (Command, bool) {
	parts := strings.Fields(text)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return Command{}, false
	}
	raw := strings.TrimPrefix(parts[0], "/")
	name, target := raw, ""
	if at := strings.IndexByte(raw, '@'); at >= 0 {
		name, target = raw[:at], strings.TrimPrefix(raw[at+1:], "@")
		if target == "" {
			return Command{}, false
		}
	}
	if name == "" {
		return Command{}, false
	}
	return Command{Name: strings.ToLower(name), Target: strings.ToLower(target), Args: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), parts[0]))}, true
}

// commandReplyError distinguishes a failed receipt from a failed command.
// Webhook callers still receive the delivery error; polling cannot retry the
// whole update just to resend a receipt for a command that already ran.
type commandReplyError struct{ err error }

func (e *commandReplyError) Error() string { return e.err.Error() }
func (e *commandReplyError) Unwrap() error { return e.err }

// Run never waits for a model response before polling a stop command. Long
// operations are bounded and registered before dispatch, so an immediately
// following stop can cancel them even before their goroutine starts.
func (ch *Channel) Run(ctx context.Context) error {
	ch.mu.Lock()
	mode := ch.mode
	ch.mu.Unlock()
	if mode != "polling" {
		return errors.New("telegram: webhook mode requires WebhookHandler")
	}
	runCtx, cancel := context.WithCancel(ctx)
	var jobs sync.WaitGroup
	defer func() { cancel(); jobs.Wait() }()

	type updateState uint8
	const (
		ready updateState = iota // seen in a batch, but not dispatched (or eligible for retry)
		pending
		completed
	)
	var mu sync.Mutex // protects the per-bot update maps and active jobs
	active := map[int64]context.CancelFunc{}
	var polledToken string
	var highestSeen int64
	var seen map[int64]updateState
	var failedAttempts map[int64]int
	const maxUpdateAttempts = 2
	waitToPoll := func() error {
		timer := time.NewTimer(ch.poll)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}
	for {
		if err := runCtx.Err(); err != nil {
			return err
		}
		ch.mu.Lock()
		mode = ch.mode
		client := ch.client
		ch.mu.Unlock()
		if mode != "polling" || client == nil {
			return nil
		}
		if seen == nil || client.token != polledToken {
			// Telegram update IDs belong to the bot, not the HTTP client.
			// Do not carry an offset over if Channel.Reload switches tokens.
			polledToken = client.token
			highestSeen = -1
			seen = make(map[int64]updateState)
			failedAttempts = make(map[int64]int)
		}
		mu.Lock()
		offset := highestSeen + 1
		for id, state := range seen {
			if state != completed && id < offset {
				offset = id
			}
		}
		mu.Unlock()
		raw, err := client.call(runCtx, "getUpdates", url.Values{"timeout": {"25"}, "offset": {strconv.FormatInt(offset, 10)}})
		if err != nil {
			if err := waitToPoll(); err != nil {
				return err
			}
			continue
		}
		var items []update
		if json.Unmarshal(raw, &items) != nil {
			return errors.New("telegram: invalid update response")
		}
		// Register the whole batch before dispatch: a later update may be
		// completed while an earlier one is still pending or waiting to retry.
		mu.Lock()
		for id := range seen {
			if id < offset {
				delete(seen, id)
				delete(failedAttempts, id)
			}
		}
		for _, u := range items {
			if u.UpdateID < offset {
				continue
			}
			if _, exists := seen[u.UpdateID]; !exists {
				seen[u.UpdateID] = ready
			}
			if u.UpdateID > highestSeen {
				highestSeen = u.UpdateID
			}
		}
		mu.Unlock()
		dispatched := false
		retryPending := false
		for _, u := range items {
			if err := runCtx.Err(); err != nil {
				return err
			}
			if u.UpdateID < offset {
				continue
			}
			mu.Lock()
			state := seen[u.UpdateID]
			mu.Unlock()
			if state != ready {
				continue
			}
			dispatched = true
			if ch.acceptsUpdate(runCtx, u) {
				command, isCommand := parseTelegramCommand(u.Message.Text)
				req, bindErr := ch.bindUpdate(runCtx, u)
				switch {
				case bindErr != nil:
					failedAttempts[u.UpdateID]++
					if failedAttempts[u.UpdateID] < maxUpdateAttempts {
						retryPending = true
						continue
					}
					_, _ = client.Send(runCtx, u.Message.Chat.ID, "无法确定当前会话，请稍后重试。", false)
				case !isCommand || command.Name == "retry":
					mu.Lock()
					if active[u.Message.Chat.ID] == nil && len(active) < 8 {
						jobCtx, jobCancel := context.WithCancel(runCtx)
						active[u.Message.Chat.ID] = jobCancel
						seen[u.UpdateID] = pending
						jobs.Add(1)
						go func(states map[int64]updateState, id, chatID int64, item Request) {
							defer jobs.Done()
							defer jobCancel()
							_ = ch.handleRequest(jobCtx, item)
							mu.Lock()
							// /stop cancels only the job, so it is still handled.
							// A canceled Run must leave unfinished updates unconfirmed.
							if runCtx.Err() == nil {
								states[id] = completed
							}
							delete(active, chatID)
							mu.Unlock()
						}(seen, u.UpdateID, u.Message.Chat.ID, req)
						mu.Unlock()
						continue
					}
					mu.Unlock()
					// A failed busy notice does not make the rejection retryable.
					_, _ = client.Send(runCtx, u.Message.Chat.ID, "当前聊天或服务正在生成，请稍后重试；可发送 /stop 停止。", false)
				default:
					// A redelivered stop receipt must not cancel a newer worker.
					if command.Name == "stop" && failedAttempts[u.UpdateID] == 0 {
						mu.Lock()
						if stop := active[u.Message.Chat.ID]; stop != nil {
							stop()
						}
						mu.Unlock()
					}
					var replyErr *commandReplyError
					if err := ch.handleRequest(runCtx, req); err != nil && !errors.Is(err, ErrUnsupported) {
						failedAttempts[u.UpdateID]++
						if failedAttempts[u.UpdateID] < maxUpdateAttempts {
							retryPending = true
							continue
						}
						// A permanently failing update must eventually release the offset.
						if !errors.As(err, &replyErr) {
							_, _ = client.Send(runCtx, req.ChatID, "命令处理失败，请发送一条新命令重试。", false)
						}
					}
				}
			}
			mu.Lock()
			if runCtx.Err() == nil {
				seen[u.UpdateID] = completed
			}
			mu.Unlock()
		}
		// Telegram immediately returns unconfirmed updates. Pace polls that
		// served only duplicates so a pending worker does not cause a hot loop.
		if retryPending || (!dispatched && len(items) != 0) {
			if err := waitToPoll(); err != nil {
				return err
			}
		}
	}
}
func (ch *Channel) requestFromUpdate(u update) Request {
	r := Request{ChatID: u.Message.Chat.ID, UserID: u.Message.From.ID, ChatType: u.Message.Chat.Type, Username: u.Message.From.Username, Name: strings.TrimSpace(u.Message.From.FirstName + " " + u.Message.From.LastName), Text: u.Message.Text, MessageID: u.Message.MessageID}
	if sender := u.Message.SenderChat; sender != nil {
		// Anonymous administrators share a compatibility From.ID. A chat
		// identity must never acquire permissions intended for one person.
		r.UserID, r.Username = 0, ""
		r.Name = strings.TrimSpace(sender.Title)
		if r.Name == "" {
			r.Name = "匿名发言人"
		}
	}
	return r
}

// acceptsUpdate filters incoming messages before they can create session state.
func (ch *Channel) acceptsUpdate(ctx context.Context, u update) bool {
	if u.Message.Chat.ID == 0 || strings.TrimSpace(u.Message.Text) == "" {
		return false
	}
	ch.mu.Lock()
	allowed := len(ch.whitelist) == 0 || (u.Message.SenderChat == nil && u.Message.From.ID != 0 && ch.whitelist[u.Message.From.ID])
	botUsername, client := ch.botUsername, ch.client
	ch.mu.Unlock()
	if !allowed {
		return false
	}
	if command, ok := parseTelegramCommand(u.Message.Text); ok {
		return command.Target == "" || strings.EqualFold(command.Target, botUsername)
	}
	switch u.Message.Chat.Type {
	case "group", "supergroup":
		return groupTriggered(ctx, u, client)
	default:
		return true
	}
}

func (ch *Channel) bindUpdate(ctx context.Context, u update) (Request, error) {
	r := ch.requestFromUpdate(u)
	ch.mu.Lock()
	handler := ch.handler
	ch.mu.Unlock()
	if binder, ok := handler.(SessionBinder); ok {
		return binder.BindSession(ctx, r)
	}
	return r, nil
}
func (ch *Channel) HandleUpdate(ctx context.Context, u update) error {
	if !ch.acceptsUpdate(ctx, u) {
		return nil
	}
	r, err := ch.bindUpdate(ctx, u)
	if err != nil {
		return err
	}
	return ch.handleRequest(ctx, r)
}
func (ch *Channel) handleRequest(ctx context.Context, r Request) error {
	ch.mu.Lock()
	client := ch.client
	handler := ch.handler
	botUsername := ch.botUsername
	allowed := len(ch.whitelist) == 0 || (r.UserID != 0 && ch.whitelist[r.UserID])
	ch.mu.Unlock()
	if !allowed || client == nil || handler == nil {
		return nil
	}
	if command, ok := parseTelegramCommand(r.Text); ok {
		if command.Target != "" && (botUsername == "" || !strings.EqualFold(command.Target, botUsername)) {
			return nil
		}
		resp, e := handler.Command(ctx, r, command)
		if e != nil {
			return e
		}
		if resp.Text != "" {
			send := func() error {
				_, sendErr := client.Send(ctx, r.ChatID, MarkdownHTML(resp.Text), true)
				if sendErr != nil {
					return &commandReplyError{err: sendErr}
				}
				return nil
			}
			if delivery, ok := handler.(CommandDelivery); ok {
				var handled bool
				handled, e = delivery.DeliverCommand(ctx, r, command, send)
				if e == nil && !handled {
					e = send()
				}
			} else {
				e = send()
			}
		}
		return e
	}
	stream := NewStreamEditor(client, r.ChatID, 1200*time.Millisecond)
	if err := stream.Start(ctx); err != nil {
		return err
	}
	resp, e := handler.Chat(ctx, r, func(text string) error {
		if text == "" {
			return nil
		}
		return stream.Push(ctx, text)
	})
	if e != nil {
		_ = stream.Abort(ctx)
		return e
	}
	return stream.Close(ctx, resp.Text)
}
func (ch *Channel) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var u update
		if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&u) != nil || u.Message.Chat.ID == 0 {
			http.Error(w, "bad update", 400)
			return
		}
		if !ch.acceptsUpdate(r.Context(), u) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		bound, bindErr := ch.bindUpdate(r.Context(), u)
		if bindErr != nil {
			http.Error(w, "update failed", 500)
			return
		}
		if e := ch.handleRequest(r.Context(), bound); e != nil && !errors.Is(e, ErrUnsupported) {
			http.Error(w, "update failed", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
func (ch *Channel) Reload(ctx context.Context, cfg map[string]any) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	token, _ := cfg["telegram.token"].(string)
	if strings.TrimSpace(token) == "" {
		return ErrNotConfigured
	}
	var httpClient *http.Client
	if ch.client != nil {
		httpClient = ch.client.http
	}
	c, e := NewClient(token, httpClient)
	if e != nil {
		return e
	}
	ch.client = c
	if m, ok := cfg["telegram.mode"].(string); ok {
		ch.mode = m
	}
	ch.botUsername = ""
	if username, ok := cfg["telegram.bot_username"].(string); ok {
		ch.botUsername = strings.TrimPrefix(strings.TrimSpace(username), "@")
	}
	ch.whitelist = map[int64]bool{}
	if raw, ok := cfg["telegram.whitelist"].(string); ok {
		for _, v := range strings.Split(raw, ",") {
			if id, e := strconv.ParseInt(strings.TrimSpace(v), 10, 64); e == nil {
				ch.whitelist[id] = true
			}
		}
	}
	_ = ctx
	return nil
}

// SendText sends one scheduled or administrative text without exposing the Bot API client.
func (ch *Channel) SendText(ctx context.Context, chatID int64, text string) error {
	ch.mu.Lock()
	client := ch.client
	ch.mu.Unlock()
	if client == nil {
		return ErrNotConfigured
	}
	_, err := client.Send(ctx, chatID, MarkdownHTML(text), true)
	return err
}

func (ch *Channel) Client() *Client { ch.mu.Lock(); defer ch.mu.Unlock(); return ch.client }
func (ch *Channel) Close() error    { return nil }
