package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func TestTelegramCatalogAnonymousSenderCannotClaimCreator(t *testing.T) {
	ctx := context.Background()
	svc, db := newTelegramIntegrationService(t)
	h := &telegramServiceHandler{service: svc}
	var replies []string
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	http.DefaultTransport = telegramTestTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		replies = append(replies, r.Form.Get("text"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":100}}`))}, nil
	})
	ch, err := telegram.New(map[string]any{"telegram.token": "123:synthetic", "telegram.mode": "webhook"}, h)
	if err != nil {
		t.Fatal(err)
	}
	for id, text := range []string{"/new", "/rename Anonymous ownership", "/retry"} {
		payload := fmt.Sprintf(`{"message":{"message_id":%d,"chat":{"id":-42,"type":"supergroup"},"from":{"id":1087968824,"is_bot":true},"sender_chat":{"id":-42,"type":"supergroup"},"text":%q}}`, id+1, text)
		w := httptest.NewRecorder()
		ch.WebhookHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(payload)))
		if w.Code != http.StatusNoContent {
			t.Fatalf("status=%d", w.Code)
		}
	}
	var pointer struct {
		UserID    int64  `json:"user_id"`
		SessionID string `json:"session_id"`
	}
	if err := db.Get(ctx, "telegram_session", "chat_-42", &pointer); err != nil {
		t.Fatal(err)
	}
	if pointer.UserID != 0 {
		t.Fatalf("anonymous command claimed personal creator %d", pointer.UserID)
	}
	if len(replies) != 3 || !strings.Contains(replies[1], "只有创建") || !strings.Contains(replies[2], "无法确认匿名发送者") {
		t.Fatalf("anonymous command replies=%q", replies)
	}
	var history sessionRecordForTest
	if err := db.Get(ctx, "session", pointer.SessionID, &history); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("anonymous rename wrote history: %v", err)
	}
}

func TestTelegramSessionCommandsThroughWebhook(t *testing.T) {
	ctx := context.Background()
	dbDir, dataDir := t.TempDir(), t.TempDir()
	vault, err := secure.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	open := func() (*core.Service, *store.Store) {
		t.Helper()
		db, err := store.Open(dbDir, vault)
		if err != nil {
			t.Fatal(err)
		}
		s, err := core.New(ctx, db, vault, secure.NewRedactor(), dataDir, nil)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		return s, db
	}
	svc, db := open()
	defer func() { svc.Close(); db.Close() }()
	if _, err = svc.Action(ctx, "accounts.import", map[string]any{"id": "synthetic", "cookies": map[string]string{"hatch_sess": "synthetic-only"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Action(ctx, "personas.save", map[string]any{"id": "calm", "name": "Calm", "prompt": "Be calm"}); err != nil {
		t.Fatal(err)
	}

	var sent []string
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	http.DefaultTransport = telegramTestTransport(func(req *http.Request) (*http.Response, error) {
		if err := req.ParseForm(); err != nil {
			return nil, err
		}
		if strings.HasSuffix(req.URL.Path, "/sendMessage") {
			sent = append(sent, req.Form.Get("text"))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":100}}`))}, nil
	})
	var prompts []string
	newHandler := func() (*telegramServiceHandler, http.Handler) {
		t.Helper()
		svc.SetGenerateForTest(func(_ context.Context, _ *hatch.Client, req hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
			prompts = append(prompts, req.Prompt)
			return hatch.GenerateResult{Text: "reply to " + req.Prompt}, nil
		})
		h := &telegramServiceHandler{service: svc}
		ch, err := telegram.New(map[string]any{"telegram.token": "123:synthetic", "telegram.mode": "webhook", "telegram.whitelist": "7,8"}, h)
		if err != nil {
			t.Fatal(err)
		}
		return h, ch.WebhookHandler()
	}
	h, webhook := newHandler()
	var messageID int
	command := func(chat, user int64, text string) string {
		t.Helper()
		messageID++
		sent = nil
		payload := fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":%d,"type":"group"},"from":{"id":%d},"text":%q}}`, messageID, messageID, chat, user, text)
		w := httptest.NewRecorder()
		webhook.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(payload)))
		if w.Code != http.StatusNoContent || len(sent) == 0 {
			t.Fatalf("%s status=%d replies=%v", text, w.Code, sent)
		}
		return strings.Join(sent, "\n")
	}
	selected := func(chat, user int64) string {
		t.Helper()
		id, err := svc.TelegramSession(ctx, chat, user)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	chat := func(chat, user int64, text string) {
		t.Helper()
		if _, err := h.Chat(ctx, telegram.Request{ChatID: chat, UserID: user, Text: text}, nil); err != nil {
			t.Fatal(err)
		}
	}

	command(-42, 7, "/new")
	first := selected(-42, 7)
	chat(-42, 7, "first message")
	if reply := command(-42, 7, "/rename First topic"); !strings.Contains(reply, "First topic") {
		t.Fatalf("rename reply=%q", reply)
	}
	command(-42, 8, "/new")
	second := selected(-42, 8)
	chat(-42, 8, "second message")
	command(-99, 7, "/new")
	foreign := selected(-99, 7)
	chat(-99, 7, "foreign secret")
	if list := command(-42, 8, "/sessions"); !strings.Contains(list, first) || !strings.Contains(list, second) || !strings.Contains(list, "First topic") || strings.Contains(list, foreign) || strings.Contains(list, "foreign secret") {
		t.Fatalf("session listing=%q", list)
	}
	if reply := command(-42, 8, "/switch "+foreign); strings.Contains(reply, "foreign secret") || selected(-42, 8) != second {
		t.Fatalf("foreign session was exposed or selected, reply=%q", reply)
	}
	if reply := command(-42, 8, "/switch "+first); !strings.Contains(reply, first) || selected(-42, 8) != first {
		t.Fatalf("switch reply=%q", reply)
	}
	if reply := command(-42, 8, "/rename Forbidden rename"); strings.Contains(reply, "Forbidden rename") {
		t.Fatalf("unexpected success=%q", reply)
	}
	if list := command(-42, 8, "/sessions"); !strings.Contains(list, "First topic") || strings.Contains(list, "Forbidden rename") {
		t.Fatalf("rename changed another creator's title: %q", list)
	}
	if history := command(-42, 8, "/history"); !strings.Contains(history, "first message") || strings.Contains(history, "second message") || strings.Contains(history, "foreign secret") {
		t.Fatalf("history=%q", history)
	}
	if reply := command(-42, 8, "/persona calm"); !strings.Contains(reply, "只有创建") {
		t.Fatalf("switch gained persona permissions: %q", reply)
	}
	if reply := command(-42, 7, "/persona calm"); !strings.Contains(reply, "人设已切换") {
		t.Fatalf("original creator lost persona permissions: %q", reply)
	}
	if reply := command(-42, 8, "/retry"); !strings.Contains(reply, "没有可重试") {
		t.Fatalf("retry used a different sender or conversation: %q", reply)
	}

	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	svc, db = open()
	h, webhook = newHandler()
	if current := selected(-42, 7); current != first {
		t.Fatalf("selected after reopening database=%q", current)
	}
	if reply := command(-42, 7, "/retry"); !strings.Contains(reply, "reply to first message") || prompts[len(prompts)-1] != "first message" {
		t.Fatalf("retry after reopen: %q prompts=%q", reply, prompts)
	}
	for _, bad := range []string{"/sessions 0", "/sessions 99999999999999999999999999999999", "/switch", "/switch one two", "/rename", "/history another-session"} {
		if reply := command(-42, 7, bad); !strings.Contains(reply, "用法") || selected(-42, 7) != first {
			t.Fatalf("invalid command %q: %q", bad, reply)
		}
	}
	if history := command(-42, 7, "/history"); !strings.Contains(history, "first message") {
		t.Fatalf("invalid command changed history: %q", history)
	}
	for i := 0; i < 9; i++ {
		command(-42, 7, "/new")
	}
	pageOne := command(-42, 7, "/sessions")
	pageTwo := command(-42, 7, "/sessions 2")
	if strings.Count(pageOne, "telegram_-42_") != 8 || !strings.Contains(pageOne, "下一页：/sessions 2") || strings.Count(pageTwo, "telegram_-42_") != 3 || strings.Contains(pageTwo, "下一页") {
		t.Fatalf("pagination:\n%s\n%s", pageOne, pageTwo)
	}
}
