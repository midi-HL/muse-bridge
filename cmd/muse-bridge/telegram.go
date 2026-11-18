package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

type telegramServiceHandler struct{ service *core.Service }

type telegramRuntime struct {
	mu      sync.Mutex
	parent  context.Context
	cancel  context.CancelFunc
	ch      *telegram.Channel
	service *core.Service
	handler telegram.Handler
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
	if strings.TrimSpace(token) == "" {
		if r.cancel != nil {
			r.cancel()
			r.cancel = nil
		}
		if r.ch != nil {
			_ = r.ch.Close()
			r.ch = nil
		}
		return nil
	}
	if r.ch != nil {
		if r.cancel != nil {
			r.cancel()
			r.cancel = nil
		}
		_ = r.ch.Close()
		r.ch = nil
	}
	ch, err := telegram.New(values, r.handler)
	if err != nil {
		return err
	}
	r.ch = ch
	pollCtx, cancel := context.WithCancel(r.parent)
	r.cancel = cancel
	go func() { _ = ch.Run(pollCtx) }()
	return nil
}

func (r *telegramRuntime) close() error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	ch := r.ch
	r.ch = nil
	r.mu.Unlock()
	if ch != nil {
		return ch.Close()
	}
	return nil
}
