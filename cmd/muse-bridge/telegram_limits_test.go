package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func dailyReceiptCount(sends, edits []string, want string) int {
	count := 0
	for _, text := range sends {
		if text == want {
			count++
		}
	}
	for _, text := range edits {
		if text == want {
			count++
		}
	}
	return count
}

func assertDailyReceiptIncrement(t *testing.T, label string, before int, sends, edits []string) {
	t.Helper()
	if after := dailyReceiptCount(sends, edits, "今日请求次数已达上限，请在配置时区的次日重试或联系管理员。"); after != before+1 {
		t.Fatalf("%s did not deliver exactly one daily-limit receipt: before=%d after=%d sends=%q edits=%q", label, before, after, sends, edits)
	}
}

func TestTelegramGroupDailyLimitSharedBySenderAcrossGroupsAndRetryReceipt(t *testing.T) {
	ctx := context.Background()
	svc, db := newTelegramIntegrationService(t)
	if _, err := svc.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 1}); err != nil {
		t.Fatal(err)
	}

	var generated []string
	svc.SetGenerateForTest(func(_ context.Context, _ *hatch.Client, req hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
		generated = append(generated, req.Prompt)
		return hatch.GenerateResult{Text: "answer: " + req.Prompt}, nil
	})

	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	var sends, edits []string
	var botLookups int
	http.DefaultTransport = telegramTestTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/getMe"):
			botLookups++
			body = `{"ok":true,"result":{"id":123,"is_bot":true,"username":"my_bot"}}`
		case strings.HasSuffix(req.URL.Path, "/sendMessage"):
			if err := req.ParseForm(); err != nil {
				return nil, err
			}
			sends = append(sends, req.Form.Get("text"))
			body = fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, len(sends))
		case strings.HasSuffix(req.URL.Path, "/editMessageText"):
			if err := req.ParseForm(); err != nil {
				return nil, err
			}
			edits = append(edits, req.Form.Get("text"))
			body = `{"ok":true,"result":true}`
		case strings.HasSuffix(req.URL.Path, "/deleteMessage"):
			body = `{"ok":true,"result":true}`
		default:
			return nil, fmt.Errorf("unexpected Telegram Bot API endpoint %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	h := &telegramServiceHandler{service: svc}
	ch, err := telegram.New(map[string]any{
		"telegram.token": "123:synthetic", "telegram.mode": "webhook", "telegram.whitelist": "12,13", "telegram.bot_username": "my_bot",
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Close() })

	deliver := func(updateID, messageID, chatID, senderID int64, name, text string, mention bool) string {
		t.Helper()
		payload := fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"from":{"id":%d,"first_name":%q},"chat":{"id":%d,"type":"group"},"text":%q`, updateID, messageID, senderID, name, chatID, text)
		if mention {
			payload += `,"entities":[{"type":"mention","offset":0,"length":7}]`
		}
		payload += `}}`
		w := httptest.NewRecorder()
		ch.WebhookHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(payload)))
		if w.Code != http.StatusNoContent {
			t.Fatalf("webhook update %d returned status=%d body=%q", updateID, w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	const dailyMessage = "今日请求次数已达上限，请在配置时区的次日重试或联系管理员。"
	deliver(1, 1, -42, 12, "Alice", "@my_bot first accepted", true)
	crossGroupBefore := dailyReceiptCount(sends, edits, dailyMessage)
	crossGroupAck := deliver(2, 2, -43, 12, "Alice", "@my_bot cross-group overage", true)
	assertDailyReceiptIncrement(t, "cross-group overage", crossGroupBefore, sends, edits)
	sameGroupBefore := dailyReceiptCount(sends, edits, dailyMessage)
	sameGroupAck := deliver(3, 3, -42, 12, "Alice", "@my_bot same-group overage", true)
	assertDailyReceiptIncrement(t, "same-group overage", sameGroupBefore, sends, edits)
	retryBefore := dailyReceiptCount(sends, edits, dailyMessage)
	retryAck := deliver(4, 4, -42, 12, "Alice", "/retry", false)
	assertDailyReceiptIncrement(t, "/retry", retryBefore, sends, edits)
	deliver(5, 5, -43, 13, "Bob", "@my_bot independent sender", true)
	// A duplicate update delivered after /new still reuses the original result;
	// the current session pointer must not affect the stable update identity.
	deliver(6, 6, -43, 13, "Bob", "/new", false)
	beforeReplay := dailyReceiptCount(sends, edits, dailyMessage)
	deliver(5, 5, -43, 13, "Bob", "@my_bot independent sender", true)
	if got := dailyReceiptCount(sends, edits, dailyMessage); got != beforeReplay {
		t.Fatalf("duplicate update changed daily-limit receipts: before=%d after=%d", beforeReplay, got)
	}

	if want := []string{"[Alice]: @my_bot first accepted", "[Bob]: @my_bot independent sender"}; fmt.Sprint(generated) != fmt.Sprint(want) {
		t.Fatalf("generated prompts=%q, want exactly the accepted Alice and Bob messages %q", generated, want)
	}
	if botLookups != 1 {
		t.Fatalf("getMe calls=%d, want one cached bot identity lookup", botLookups)
	}
	if crossGroupAck != "" || sameGroupAck != "" || retryAck != "" {
		t.Fatalf("webhook acknowledgments should be empty 204 bodies: cross-group=%q same-group=%q retry=%q", crossGroupAck, sameGroupAck, retryAck)
	}

	foundDailyReceipt := false
	for _, text := range sends {
		if text == dailyMessage {
			foundDailyReceipt = true
		}
	}
	for _, text := range edits {
		if text == dailyMessage {
			foundDailyReceipt = true
		}
	}
	if !foundDailyReceipt {
		t.Fatalf("webhook did not deliver an explicit translated daily-limit receipt; sends=%q edits=%q", sends, edits)
	}

	const aliceSession = "telegram_-42"
	if prompt, err := svc.TelegramLastPrompt(ctx, aliceSession, "12"); err != nil || prompt != "[Alice]: @my_bot first accepted" {
		t.Fatalf("Alice retry input=%q err=%v; rejected requests must preserve the successful retry target", prompt, err)
	}
	var aliceHistory sessionRecordForTest
	if err := db.Get(ctx, "session", aliceSession, &aliceHistory); err != nil {
		t.Fatal(err)
	}
	wantAliceHistory := []hatch.Message{{Role: "user", Content: "[Alice]: @my_bot first accepted"}, {Role: "assistant", Content: "answer: [Alice]: @my_bot first accepted"}}
	if fmt.Sprint(aliceHistory.Messages) != fmt.Sprint(wantAliceHistory) {
		t.Fatalf("Alice history changed after quota rejections and /retry: %#v", aliceHistory.Messages)
	}
	var groupTwoHistory sessionRecordForTest
	if err := db.Get(ctx, "session", "telegram_-43", &groupTwoHistory); err != nil {
		t.Fatal(err)
	}
	if len(groupTwoHistory.Messages) != 2 || groupTwoHistory.Messages[0].Content != "[Bob]: @my_bot independent sender" {
		t.Fatalf("second-group history should contain only Bob's accepted turn: %#v", groupTwoHistory.Messages)
	}
	if _, err := svc.TelegramRetryAttempt(ctx, "telegram_-43", "12"); err == nil {
		t.Fatal("Alice's rejected cross-group request unexpectedly became retryable")
	}
}
