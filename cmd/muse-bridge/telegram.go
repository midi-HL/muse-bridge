package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
)

type telegramServiceHandler struct{ service *core.Service }

func (h *telegramServiceHandler) currentSession(ctx context.Context, r telegram.Request) (string, error) {
	if r.SessionID != "" {
		return r.SessionID, nil
	}
	return h.service.TelegramSession(ctx, r.ChatID, r.UserID)
}
func (h *telegramServiceHandler) BindSession(ctx context.Context, r telegram.Request) (telegram.Request, error) {
	id, err := h.currentSession(ctx, r)
	r.SessionID = id
	return r, err
}
func (h *telegramServiceHandler) newSession(ctx context.Context, r telegram.Request) (string, error) {
	return h.service.NewTelegramSession(ctx, r.ChatID, r.UserID)
}

type telegramRuntime struct {
	mu               sync.Mutex
	parent           context.Context
	cancel           context.CancelFunc
	ch               *telegram.Channel
	service          *core.Service
	handler          telegram.Handler
	token            string
	secret           string
	mode             string
	webhookURL       string
	whitelist        string
	botUsername      string
	disabled         bool
	whitelistEnabled bool
}

type telegramConfig struct {
	token, mode, secret, webhookURL, whitelist, botUsername string
	disabled, whitelistEnabled                              bool
}

func readTelegramConfig(values map[string]any) telegramConfig {
	cfg := telegramConfig{}
	cfg.token, _ = values["telegram.token"].(string)
	cfg.mode, _ = values["telegram.mode"].(string)
	cfg.secret, _ = values["telegram.webhook_secret"].(string)
	cfg.webhookURL, _ = values["telegram.webhook_url"].(string)
	cfg.whitelist, _ = values["telegram.whitelist"].(string)
	cfg.botUsername, _ = values["telegram.bot_username"].(string)
	if enabled, configured := values["telegram.enabled"].(bool); configured {
		cfg.disabled = !enabled
	}
	cfg.whitelistEnabled, _ = values["telegram.whitelist_enabled"].(bool)
	return cfg
}
func (r *telegramRuntime) sameConfig(cfg telegramConfig) bool {
	return r.token == cfg.token && r.mode == cfg.mode && r.secret == cfg.secret && r.webhookURL == cfg.webhookURL && r.whitelist == cfg.whitelist && r.botUsername == cfg.botUsername && r.disabled == cfg.disabled && r.whitelistEnabled == cfg.whitelistEnabled
}

func (h *telegramServiceHandler) Chat(ctx context.Context, r telegram.Request, emit func(string) error) (telegram.Response, error) {
	return h.ChatWithStatus(ctx, r, emit, nil)
}

func (h *telegramServiceHandler) ChatWithStatus(ctx context.Context, r telegram.Request, emit func(string) error, status func(context.Context, string) error) (telegram.Response, error) {
	session, err := h.currentSession(ctx, r)
	if err != nil {
		return telegram.Response{}, err
	}
	owner := strconv.FormatInt(r.ChatID, 10)
	req := core.ChatRequest{
		SessionID:   session,
		Platform:    "telegram",
		UserID:      owner,
		Prompt:      telegramPrompt(r),
		RetryUserID: strconv.FormatInt(r.UserID, 10),
		QueueStatus: func(runCtx context.Context, text string) error {
			if status == nil {
				return nil
			}
			notifyCtx, cancel := context.WithTimeout(runCtx, 2*time.Second)
			defer cancel()
			return status(notifyCtx, text)
		},
	}
	generate := func() (string, error) {
		result, err := h.service.Chat(ctx, req, func(ev core.ChatEvent) error {
			if ev.Type == "text" && ev.Text != "" && emit != nil {
				return emit(ev.Text)
			}
			return nil
		})
		return result.Text, err
	}
	var answer string
	if r.UpdateID > 0 {
		answer, err = h.service.TelegramMessageOnce(ctx, r.ChatID, r.UpdateID, r.MessageID, r.UserID, session, generate)
	} else {
		answer, err = generate()
	}
	if message := core.ChatLimitMessage(err); message != "" {
		return telegram.Response{Text: message}, nil
	}
	return telegram.Response{Text: answer}, err
}

func telegramPrompt(r telegram.Request) string {
	if r.ChatType != "group" && r.ChatType != "supergroup" {
		return r.Text
	}
	name := strings.Join(strings.FieldsFunc(r.Name, func(c rune) bool {
		return unicode.IsSpace(c) || unicode.IsControl(c) || unicode.Is(unicode.Cf, c) || strings.ContainsRune("[]()@", c)
	}), " ")
	username := strings.TrimPrefix(r.Username, "@")
	if strings.IndexFunc(username, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_')
	}) >= 0 {
		username = ""
	}
	if name == "" {
		switch {
		case username != "":
			name = username
		case r.UserID != 0:
			name = "用户 " + strconv.FormatInt(r.UserID, 10)
		default:
			name = "未知发言人"
		}
	}
	if username != "" {
		name += " (@" + username + ")"
	}
	return "[" + name + "]: " + r.Text
}

func (h *telegramServiceHandler) DeliverCommand(ctx context.Context, r telegram.Request, cmd telegram.Command, send func() error) (bool, error) {
	return h.service.TelegramCommandDeliver(ctx, r.ChatID, r.UserID, r.MessageID, cmd.Name, send)
}

func (h *telegramServiceHandler) Command(ctx context.Context, r telegram.Request, cmd telegram.Command) (telegram.Response, error) {
	session, err := h.currentSession(ctx, r)
	if err != nil {
		return telegram.Response{}, err
	}
	text, err := h.service.TelegramCommandOnce(ctx, session, r.ChatID, r.UserID, r.MessageID, cmd.Name, func() (string, error) { resp, e := h.command(ctx, r, cmd, session); return resp.Text, e })
	return telegram.Response{Text: text}, err
}
func (h *telegramServiceHandler) command(ctx context.Context, r telegram.Request, cmd telegram.Command, session string) (telegram.Response, error) {
	switch cmd.Name {
	case "new":
		if _, err := h.newSession(ctx, r); err != nil {
			return telegram.Response{}, err
		}
		return telegram.Response{Text: "已创建新会话。"}, nil
	case "sessions", "switch", "rename", "history":
		return h.sessionCommand(ctx, r, cmd, session)
	case "retry":
		if r.UserID == 0 {
			return telegram.Response{Text: "无法确认匿名发送者，不能重试个人输入。请以个人身份发送。"}, nil
		}
		attemptID, err := h.service.TelegramRetryAttempt(ctx, session, strconv.FormatInt(r.UserID, 10))
		if err != nil {
			return telegram.Response{Text: "没有可重试的上一条请求。"}, nil
		}
		result, err := h.service.Chat(ctx, core.ChatRequest{SessionID: session, Platform: "telegram", UserID: strconv.FormatInt(r.ChatID, 10), RetryUserID: strconv.FormatInt(r.UserID, 10), RetryAttemptID: attemptID, Retry: true}, nil)
		if message := core.ChatLimitMessage(err); message != "" {
			return telegram.Response{Text: message}, nil
		}
		return telegram.Response{Text: result.Text}, err
	case "stop":
		return telegram.Response{Text: map[bool]string{true: "已请求停止当前聊天的生成。", false: "当前没有正在生成的请求。"}[h.service.StopTelegramChat(r.ChatID)]}, nil
	case "status":
		state, err := h.service.State(ctx)
		if err != nil {
			return telegram.Response{}, err
		}
		sessions, _ := state["sessions"].([]map[string]any)
		return telegram.Response{Text: fmt.Sprintf("账号：%v\n排队：%v\n会话：%v", state["account_count"], state["queue_depth"], len(sessions))}, nil
	case "persona":
		text, err := h.service.TelegramPersonas(ctx, session, strconv.FormatInt(r.UserID, 10), cmd.Args)
		return telegram.Response{Text: text}, err
	case "usage":
		return h.usage(ctx, r, cmd.Args)
	case "help":
		return telegram.Response{Text: "/new 新会话\n/sessions [页码] 本聊天的本地会话\n/switch 会话ID 切换本聊天会话\n/rename 名称 重命名当前会话（仅创建者）\n/history 当前会话最近 10 条消息\n/stop 停止生成\n/status 查看状态\n/usage 查看本地每日聊天额度\n/retry 重试上一条请求\n/persona 查看人设，/persona ID 切换，/persona none 关闭\n/help 查看帮助\n/tasks 当前上游协议未暴露此能力。"}, nil
	case "tasks":
		return telegram.Response{}, telegram.ErrUnsupported
	default:
		return telegram.Response{Text: "未知命令，发送 /help 查看可用命令。"}, nil
	}
}

func (h *telegramServiceHandler) usage(ctx context.Context, r telegram.Request, args string) (telegram.Response, error) {
	if strings.TrimSpace(args) != "" {
		return telegram.Response{Text: "/usage 不接受参数，只能查询自己的本地每日额度。"}, nil
	}
	u, err := h.service.TelegramUsage(ctx, r.ChatID, r.UserID)
	if err != nil {
		if ctx.Err() != nil {
			return telegram.Response{}, ctx.Err()
		}
		return telegram.Response{Text: "暂时无法读取本地额度，请稍后发送新的 /usage 命令。"}, nil
	}
	scope := "个人"
	if r.UserID == 0 {
		scope = "本聊天匿名发送者共享"
	}
	limit, remaining := "不限", "不限"
	if u.Limit > 0 {
		limit = strconv.Itoa(u.Limit)
		remaining = strconv.Itoa(max(0, u.Limit-u.Used))
	}
	return telegram.Response{Text: fmt.Sprintf("本地每日聊天额度（%s）\n已用：%d\n上限：%s\n剩余：%s\n重置：%s（%s）\n这不是 Muse 官方账号额度。", scope, u.Used, limit, remaining, u.ResetsAt.Format("2006-01-02 15:04 -07:00"), u.Timezone)}, nil
}

func (h *telegramServiceHandler) Sticker(context.Context, string) (telegram.StickerAsset, error) {
	return telegram.StickerAsset{}, telegram.ErrUnsupported
}
func (h *telegramServiceHandler) Feedback(context.Context, telegram.Request, telegram.Delivery) error {
	return nil
}

func newTelegramRuntime(ctx context.Context, service *core.Service) *telegramRuntime {
	return &telegramRuntime{parent: ctx, service: service, handler: &telegramServiceHandler{service: service}}
}

func (r *telegramRuntime) apply(values map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg := readTelegramConfig(values)
	token, mode, secret, webhookURL := cfg.token, cfg.mode, cfg.secret, cfg.webhookURL
	if r.ch != nil && r.sameConfig(cfg) {
		return nil
	}
	if cfg.disabled {
		return r.stopLocked()
	}
	if mode == "webhook" && strings.TrimSpace(webhookURL) == "" && strings.TrimSpace(token) != "" {
		return errors.New("telegram: webhook mode requires telegram.webhook_url")
	}
	if err := r.stopLocked(); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return nil
	}
	ch, err := telegram.New(values, r.handler)
	if err != nil {
		return err
	}
	r.ch = ch
	r.token = token
	r.secret = secret
	r.mode = mode
	r.webhookURL = webhookURL
	r.whitelist = cfg.whitelist
	r.botUsername = cfg.botUsername
	r.disabled, r.whitelistEnabled = cfg.disabled, cfg.whitelistEnabled
	if mode == "webhook" {
		if err := r.registerWebhookLocked(); err != nil {
			_ = r.stopLocked()
			return err
		}
		return nil
	}
	pollCtx, cancel := context.WithCancel(r.parent)
	r.cancel = cancel
	go func() { _ = ch.Run(pollCtx) }()
	return nil
}

// stopLocked clears Telegram's remote webhook before replacing or disabling a
// channel. Keeping this operation under the runtime lock prevents a stale
// webhook handler from racing a settings reload.
func (r *telegramRuntime) stopLocked() error {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.ch == nil {
		r.token = ""
		r.secret = ""
		r.mode = ""
		r.webhookURL = ""
		r.whitelist = ""
		return nil
	}
	if r.mode == "webhook" {
		ctx, cancel := context.WithTimeout(r.parent, 15*time.Second)
		err := r.ch.Client().DeleteWebhook(ctx)
		cancel()
		if err != nil {
			return err
		}
	}
	_ = r.ch.Close()
	r.ch = nil
	r.token = ""
	r.secret = ""
	r.mode = ""
	r.webhookURL = ""
	r.whitelist = ""
	return nil
}

func (r *telegramRuntime) sendScheduled(ctx context.Context, job core.ScheduleExecution) error {
	if job.Platform != "telegram" {
		return errors.New("scheduled platform is not connected")
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(job.Target), 10, 64)
	if err != nil || chatID == 0 {
		return errors.New("scheduled Telegram target is invalid")
	}
	r.mu.Lock()
	ch := r.ch
	mode := r.mode
	r.mu.Unlock()
	if ch == nil || mode == "" {
		return telegram.ErrNotConfigured
	}
	return ch.SendText(ctx, chatID, job.Prompt)
}

func (r *telegramRuntime) registerWebhookLocked() error {
	if r.ch == nil {
		return errors.New("telegram: channel is not initialized")
	}
	if strings.TrimSpace(r.webhookURL) == "" {
		return errors.New("telegram: webhook URL is required")
	}
	ctx, cancel := context.WithTimeout(r.parent, 15*time.Second)
	defer cancel()
	return r.ch.Client().SetWebhook(ctx, r.webhookURL, r.secret)
}

func (r *telegramRuntime) webhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		ch := r.ch
		r.mu.Unlock()
		if ch == nil {
			http.NotFound(w, req)
			return
		}
		ch.WebhookHandler().ServeHTTP(w, req)
	})
}
func (r *telegramRuntime) webhookSecret() string { r.mu.Lock(); defer r.mu.Unlock(); return r.secret }

func (r *telegramRuntime) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopLocked()
}
