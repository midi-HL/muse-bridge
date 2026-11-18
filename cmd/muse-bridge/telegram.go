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

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

type telegramServiceHandler struct{ service *core.Service }

type telegramRuntime struct {
	mu         sync.Mutex
	parent     context.Context
	cancel     context.CancelFunc
	ch         *telegram.Channel
	service    *core.Service
	handler    telegram.Handler
	secret     string
	mode       string
	webhookURL string
	started    bool
}

func (h telegramServiceHandler) Chat(ctx context.Context, r telegram.Request, emit func(string) error) (telegram.Response, error) {
	session := fmt.Sprintf("telegram_%d", r.ChatID)
	result, err := h.service.Chat(ctx, core.ChatRequest{SessionID: session, Platform: "telegram", UserID: strconv.FormatInt(r.UserID, 10), Prompt: r.Text}, func(ev core.ChatEvent) error {
		if ev.Type == "text" && ev.Text != "" {
			return emit(ev.Text)
		}
		return nil
	})
	return telegram.Response{Text: result.Text}, err
}

func (h telegramServiceHandler) Command(ctx context.Context, r telegram.Request, cmd telegram.Command) (telegram.Response, error) {
	session := fmt.Sprintf("telegram_%d", r.ChatID)
	switch cmd.Name {
	case "new":
		session = "telegram_" + secure.ID()
		return telegram.Response{Text: "已创建新会话。"}, nil
	case "stop":
		return telegram.Response{Text: map[bool]string{true: "已请求停止当前生成。", false: "当前没有正在生成的请求。"}[h.service.Stop(session)]}, nil
	case "status":
		state, err := h.service.State(ctx)
		if err != nil {
			return telegram.Response{}, err
		}
		sessions, _ := state["sessions"].([]map[string]any)
		return telegram.Response{Text: fmt.Sprintf("账号：%v\n排队：%v\n会话：%v", state["account_count"], state["queue_depth"], len(sessions))}, nil
	case "help":
		return telegram.Response{Text: "/new 新会话\n/stop 停止生成\n/status 查看状态\n/help 查看帮助\n/tasks 当前上游协议未暴露此能力。"}, nil
	case "tasks", "usage":
		return telegram.Response{}, telegram.ErrUnsupported
	default:
		return telegram.Response{Text: "未知命令，发送 /help 查看可用命令。"}, nil
	}
}

func (h telegramServiceHandler) Sticker(context.Context, string) (telegram.StickerAsset, error) {
	return telegram.StickerAsset{}, telegram.ErrUnsupported
}
func (h telegramServiceHandler) Feedback(context.Context, telegram.Request, telegram.Delivery) error {
	return nil
}

func newTelegramRuntime(ctx context.Context, service *core.Service) *telegramRuntime {
	return &telegramRuntime{parent: ctx, service: service, handler: telegramServiceHandler{service: service}}
}

func (r *telegramRuntime) apply(values map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	token, _ := values["telegram.token"].(string)
	mode, _ := values["telegram.mode"].(string)
	secret, _ := values["telegram.webhook_secret"].(string)
	webhookURL, _ := values["telegram.webhook_url"].(string)
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
	r.secret = secret
	r.mode = mode
	r.webhookURL = webhookURL
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
		r.secret = ""
		r.mode = ""
		r.webhookURL = ""
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
	r.secret = ""
	r.mode = ""
	r.webhookURL = ""
	return nil
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
