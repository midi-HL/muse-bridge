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
var ErrNotConfigured = errors.New("telegram: bot token is not configured")

type Request struct {
	ChatID                         int64
	UserID                         int64
	ChatType, Username, Name, Text string
	MessageID, ThreadID            int
	Attachments                    []media.Attachment
}
type Response struct {
	Text        string
	Attachments []media.Attachment
}
type Command struct{ Name, Args string }
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
	mu          sync.Mutex
	offset      int64
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
	chunks := Split(text, 4096)
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
		Text string `json:"text"`
	} `json:"message"`
}
type Channel struct {
	client    *Client
	handler   Handler
	whitelist map[int64]bool
	mode      string
	poll      time.Duration
	mu        sync.Mutex
}

func New(cfg map[string]any, h Handler) (*Channel, error) {
	token, _ := cfg["telegram.token"].(string)
	c, e := NewClient(token, nil)
	if e != nil {
		return nil, e
	}
	ch := &Channel{client: c, handler: h, mode: "polling", poll: 1 * time.Second, whitelist: map[int64]bool{}}
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
func (ch *Channel) Run(ctx context.Context) error {
	if ch.mode != "polling" {
		return errors.New("telegram: webhook mode requires WebhookHandler")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		v, e := ch.client.call(ctx, "getUpdates", url.Values{"timeout": {"25"}, "offset": {strconv.FormatInt(ch.client.offset, 10)}})
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(ch.poll)
			continue
		}
		var items []update
		_ = json.Unmarshal(v, &items)
		for _, u := range items {
			ch.client.offset = u.UpdateID + 1
			_ = ch.HandleUpdate(ctx, u)
		}
	}
}
func (ch *Channel) HandleUpdate(ctx context.Context, u update) error {
	if len(ch.whitelist) > 0 && !ch.whitelist[u.Message.From.ID] {
		return nil
	}
	r := Request{ChatID: u.Message.Chat.ID, UserID: u.Message.From.ID, ChatType: u.Message.Chat.Type, Username: u.Message.From.Username, Name: strings.TrimSpace(u.Message.From.FirstName + " " + u.Message.From.LastName), Text: u.Message.Text, MessageID: u.Message.MessageID}
	if strings.HasPrefix(r.Text, "/") {
		parts := strings.Fields(r.Text)
		name := strings.TrimPrefix(parts[0], "/")
		args := strings.TrimSpace(strings.TrimPrefix(r.Text, parts[0]))
		resp, e := ch.handler.Command(ctx, r, Command{Name: name, Args: args})
		if e != nil {
			return e
		}
		if resp.Text != "" {
			_, e = ch.client.Send(ctx, r.ChatID, MarkdownHTML(resp.Text), true)
		}
		return e
	}
	emitted := false
	resp, e := ch.handler.Chat(ctx, r, func(text string) error {
		if text == "" {
			return nil
		}
		emitted = true
		_, e := ch.client.Send(ctx, r.ChatID, MarkdownHTML(text), true)
		return e
	})
	if e != nil {
		return e
	}
	if !emitted && resp.Text != "" {
		_, e = ch.client.Send(ctx, r.ChatID, MarkdownHTML(resp.Text), true)
	}
	return e
}
func (ch *Channel) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var u update
		if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&u) != nil {
			http.Error(w, "bad update", 400)
			return
		}
		if e := ch.HandleUpdate(r.Context(), u); e != nil && !errors.Is(e, ErrUnsupported) {
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
	if token == "" {
		return errors.New("telegram: bot token required")
	}
	c, e := NewClient(token, ch.client.http)
	if e != nil {
		return e
	}
	ch.client = c
	if m, ok := cfg["telegram.mode"].(string); ok {
		ch.mode = m
	}
	return nil
}
func (ch *Channel) Client() *Client { return ch.client }
func (ch *Channel) Close() error    { return nil }
