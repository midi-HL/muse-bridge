package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
)

func (h *telegramServiceHandler) sessionCommand(ctx context.Context, r telegram.Request, cmd telegram.Command, session string) (telegram.Response, error) {
	const pageSize = 8
	args := strings.TrimSpace(cmd.Args)
	switch cmd.Name {
	case "sessions":
		page := 1
		if args != "" {
			var err error
			page, err = strconv.Atoi(args)
			if err != nil || page < 1 {
				return telegram.Response{Text: "用法：/sessions [页码]"}, nil
			}
		}
		items, err := h.service.ListTelegramSessions(ctx, r.ChatID)
		if err != nil {
			return telegram.Response{}, err
		}
		pages := max(1, (len(items)+pageSize-1)/pageSize)
		if page > pages {
			return telegram.Response{Text: fmt.Sprintf("会话列表共 %d 页。用法：/sessions [页码]", pages)}, nil
		}
		var text strings.Builder
		fmt.Fprintf(&text, "本聊天的本地会话（第 %d/%d 页）：\n", page, pages)
		start := (page - 1) * pageSize
		for _, item := range items[start:min(start+pageSize, len(items))] {
			marker := ""
			if item.Current {
				marker = " [当前]"
			}
			title := catalogDisplayText(item.Title, 80)
			if title == "" {
				title = "未命名会话"
			}
			fmt.Fprintf(&text, "%s%s\n%s\n", title, marker, item.ID)
		}
		text.WriteString("使用 /switch 会话ID 切换；群聊中会影响整个聊天。")
		if page < pages {
			fmt.Fprintf(&text, "\n下一页：/sessions %d", page+1)
		}
		return telegram.Response{Text: text.String()}, nil
	case "switch":
		if args == "" || len(strings.Fields(args)) != 1 {
			return telegram.Response{Text: "用法：/switch 会话ID（使用 /sessions 查看）。群聊中会影响整个聊天。"}, nil
		}
		if err := h.service.SwitchTelegramSession(ctx, r.ChatID, args); err != nil {
			return catalogCommandError(err)
		}
		return telegram.Response{Text: "已切换本聊天的会话：" + args}, nil
	case "rename":
		if err := h.service.RenameTelegramSession(ctx, r.ChatID, r.UserID, session, args); err != nil {
			return catalogCommandError(err)
		}
		return telegram.Response{Text: "会话已命名为：" + args}, nil
	case "history":
		if args != "" {
			return telegram.Response{Text: "用法：/history（当前会话最近 10 条消息）。"}, nil
		}
		messages, err := h.service.TelegramSessionHistory(ctx, r.ChatID, session)
		if err != nil {
			return catalogCommandError(err)
		}
		if len(messages) == 0 {
			return telegram.Response{Text: "当前会话暂无已完成的聊天记录。"}, nil
		}
		var text strings.Builder
		text.WriteString("当前本地会话最近消息（每条最多显示 250 字符）：\n")
		for _, message := range messages {
			role := "用户"
			if message.Role == "assistant" {
				role = "Muse"
			}
			fmt.Fprintf(&text, "\n%s：%s\n", role, catalogHistoryText(message.Content, 250))
		}
		return telegram.Response{Text: text.String()}, nil
	}
	return telegram.Response{}, telegram.ErrUnsupported
}

func catalogCommandError(err error) (telegram.Response, error) {
	switch {
	case errors.Is(err, core.ErrTelegramSessionUnavailable):
		return telegram.Response{Text: "本聊天中找不到该会话，或当前会话已改变。请用 /sessions 查看。"}, nil
	case errors.Is(err, core.ErrTelegramSessionCreator):
		return telegram.Response{Text: "只有创建该会话的用户可以重命名。旧会话若没有创建者记录，请用 /new 新建会话。"}, nil
	case errors.Is(err, core.ErrTelegramSessionBusy):
		return telegram.Response{Text: "会话正在生成，请先 /stop，等生成结束后再重命名。"}, nil
	case errors.Is(err, core.ErrTelegramSessionTitle):
		return telegram.Response{Text: "用法：/rename 新名称（单行，1–80 个可见字符）。"}, nil
	default:
		return telegram.Response{}, err
	}
}

func catalogDisplayText(text string, limit int) string {
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }), " ")
	return catalogHistoryText(text, limit)
}

func catalogHistoryText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}
