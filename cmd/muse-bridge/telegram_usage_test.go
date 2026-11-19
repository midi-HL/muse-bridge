package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func TestTelegramUsageThroughWebhook(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTelegramIntegrationService(t)
	// Choose a zone safely away from midnight while using real counters.
	zone := "UTC"
	if hour := time.Now().UTC().Hour(); hour >= 23 || hour == 0 {
		zone = "Asia/Kuala_Lumpur"
	}
	if _, err := svc.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 2, "general.timezone": zone}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc.SetGenerateForTest(func(context.Context, *hatch.Client, hatch.GenerateRequest, func(hatch.Event) error) (hatch.GenerateResult, error) {
		calls++
		return hatch.GenerateResult{Text: "ok"}, nil
	})
	if _, err := svc.Chat(ctx, core.ChatRequest{Platform: "telegram", UserID: "-42", RetryUserID: "7", SessionID: "telegram_-42", Prompt: "accepted"}, nil); err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	var sent []string
	http.DefaultTransport = telegramTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			return nil, fmt.Errorf("unexpected Bot API operation")
		}
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		sent = append(sent, r.Form.Get("text"))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":100}}`))}, nil
	})
	ch, err := telegram.New(map[string]any{"telegram.token": "123:synthetic", "telegram.mode": "webhook", "telegram.whitelist": "7,8", "telegram.bot_username": "my_bot"}, &telegramServiceHandler{service: svc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	check := func(id, sender int, text string, want ...string) {
		t.Helper()
		payload := fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"from":{"id":%d},"chat":{"id":-43,"type":"group"},"text":%q}}`, id, id, sender, text)
		before := len(sent)
		w := httptest.NewRecorder()
		ch.WebhookHandler().ServeHTTP(w, httptest.NewRequest("POST", "/telegram/webhook", strings.NewReader(payload)))
		if w.Code != 204 || len(sent)-before != min(1, len(want)) {
			t.Fatalf("%s: HTTP %d, replies %q", text, w.Code, sent[before:])
		}
		for _, fragment := range want {
			if !strings.Contains(sent[before], fragment) {
				t.Fatalf("%s: reply %q missing %q", text, sent[before], fragment)
			}
		}
	}
	check(1, 7, "/usage@my_bot", "已用：1", "上限：2", "剩余：1", "重置：", zone, "不是 Muse")
	check(2, 8, "/usage", "已用：0", "剩余：2")
	check(3, 8, "/usage 7", "不接受参数")
	check(4, 9, "/usage")
	check(5, 7, "/usage@another_bot")
	check(1, 7, "/usage@my_bot") // Already delivered.
	check(6, 7, "/usage", "已用：1", "剩余：1")
	check(7, 7, "/help", "/usage 查看本地每日聊天额度")
	if _, err := svc.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 1}); err != nil {
		t.Fatal(err)
	}
	check(8, 7, "/usage", "已用：1", "上限：1", "剩余：0")
	if _, err := svc.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 0}); err != nil {
		t.Fatal(err)
	}
	check(9, 7, "/usage", "已用：1", "上限：不限", "剩余：不限")
	if calls != 1 {
		t.Fatalf("generation calls = %d, want 1", calls)
	}
}
