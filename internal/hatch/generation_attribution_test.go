package hatch

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// These fixtures travel through the real Noise transport and subscription reader.
// A subscription replays workspace history before the current task is submitted;
// media events in that history need not contain a device_id.
func attributionEvent(name, messageID, device string, extra map[string]any) map[string]any {
	context := map[string]any{
		"chat_id": "fixture-chat", "chat_kind": "main", "event_kind": "message",
		"originating_channel": "web", "require_mention": false,
	}
	if device != "" {
		context["device_id"] = device
	}
	payload := map[string]any{"message_id": messageID, "chat_context": context}
	for key, value := range extra {
		payload[key] = value
	}
	return map[string]any{"event": name, "payload": payload}
}

func attributionMedia(messageID, device, filename, mime string) map[string]any {
	return attributionEvent("delta.presentation", messageID, device, map[string]any{
		"data": map[string]any{
			"resource": map[string]any{
				"path":     "sandbox://workspace/output/" + filename,
				"filename": filename, "mime_type": mime,
			},
		},
	})
}

func attributionDone(messageID, device, transcript string) map[string]any {
	return attributionEvent("delta.message_done", messageID, device, map[string]any{
		"status": "completed",
		"transcript": map[string]any{"messages": []any{
			map[string]any{"content": []any{map[string]any{"type": "text", "text": transcript}}},
		}},
	})
}

func runAttributionGeneration(t *testing.T, m *mockHatch, model string) (GenerateResult, []Event) {
	t.Helper()
	c := New(Config{BaseURL: m.server.URL, WSURL: m.wsURL})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []Event
	result, err := c.Generate(ctx, GenerateRequest{NodeID: "node-current", Model: model, Prompt: "Reply with exactly BRIDGE_DIAG_OK"}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("generation failed: %v", err)
	}
	return result, events
}

func TestGenerateIgnoresUnattributedSubscriptionHistory(t *testing.T) {
	m := newMockHatch(t)
	defer m.close()
	for i := 0; i < 4; i++ {
		m.subscribeEvents = append(m.subscribeEvents, attributionMedia(
			fmt.Sprintf("history-message-%d", i), "", fmt.Sprintf("history-%d.png", i), "image/png",
		))
	}
	m.subscribeEvents = append(m.subscribeEvents, attributionDone("history-message-3", "", "OLD_HISTORY_TEXT"))
	m.streamEvents = func(node string) []map[string]any {
		return []map[string]any{
			attributionEvent("message.user", "current-message", node, nil),
			attributionEvent("delta.text_append", "current-message", node, map[string]any{"text": "BRIDGE_DIAG_OK"}),
			attributionDone("current-message", node, "BRIDGE_DIAG_OK"),
		}
	}
	result, events := runAttributionGeneration(t, m, "muse-spark")
	if result.Text != "BRIDGE_DIAG_OK" {
		t.Errorf("current reply = %q", result.Text)
	}
	if len(result.Attachments) != 0 {
		t.Errorf("subscription history leaked %d attachments into current result", len(result.Attachments))
	}
	for _, event := range events {
		if event.Type == "attachment" || event.Attachment != nil {
			t.Error("subscription history emitted a current attachment callback")
		}
	}
}

func TestGenerateMediaWaitsForAttributedCurrentResult(t *testing.T) {
	for _, test := range []struct{ model, filename, mime string }{
		{"muse-image", "current.png", "image/png"},
		{"muse-video", "current.mp4", "video/mp4"},
	} {
		t.Run(test.model, func(t *testing.T) {
			m := newMockHatch(t)
			defer m.close()
			m.subscribeEvents = []map[string]any{
				attributionMedia("history-message", "", "history-"+test.filename, test.mime),
				attributionDone("history-message", "", "OLD_HISTORY_TEXT"),
			}
			m.streamEvents = func(node string) []map[string]any {
				return []map[string]any{
					attributionEvent("delta.text_append", "current-message", node, map[string]any{"text": "BRIDGE_DIAG_OK"}),
					attributionMedia("current-message", node, test.filename, test.mime),
					attributionDone("current-message", node, "BRIDGE_DIAG_OK"),
				}
			}
			result, events := runAttributionGeneration(t, m, test.model)
			if result.Text != "BRIDGE_DIAG_OK" {
				t.Errorf("history completed the task before current reply: text = %q", result.Text)
			}
			assertCurrentAttachment(t, result, events, test.filename)
		})
	}
}

func TestGenerateKeepsAttributedCurrentMedia(t *testing.T) {
	m := newMockHatch(t)
	defer m.close()
	m.streamEvents = func(node string) []map[string]any {
		return []map[string]any{
			attributionMedia("current-message", node, "current.png", "image/png"),
			attributionDone("current-message", node, ""),
		}
	}
	result, events := runAttributionGeneration(t, m, "muse-image")
	assertCurrentAttachment(t, result, events, "current.png")
}

func TestGenerateCorrelatesMediaByConfirmedMessageID(t *testing.T) {
	for _, beforeConfirmation := range []bool{false, true} {
		t.Run(fmt.Sprintf("presentation_before_confirmation=%t", beforeConfirmation), func(t *testing.T) {
			m := newMockHatch(t)
			defer m.close()
			m.streamEvents = func(node string) []map[string]any {
				confirmation := attributionEvent("delta.text_append", "current-message", node, map[string]any{"text": "BRIDGE_DIAG_OK"})
				media := attributionMedia("current-message", "", "current.png", "image/png")
				events := []map[string]any{confirmation, media}
				if beforeConfirmation {
					events = []map[string]any{media, confirmation}
				}
				return append(events, attributionDone("current-message", node, "BRIDGE_DIAG_OK"))
			}
			result, events := runAttributionGeneration(t, m, "muse-image")
			if result.Text != "BRIDGE_DIAG_OK" {
				t.Errorf("presentation was accepted before its message was attributed: text = %q", result.Text)
			}
			assertCurrentAttachment(t, result, events, "current.png")
		})
	}
}

func TestGenerateRejectsForeignDeviceWithConfirmedMessageID(t *testing.T) {
	m := newMockHatch(t)
	defer m.close()
	m.streamEvents = func(node string) []map[string]any {
		return []map[string]any{
			attributionEvent("delta.text_append", "current-message", node, map[string]any{"text": "BRIDGE_DIAG_OK"}),
			attributionMedia("current-message", "foreign-device", "foreign.png", "image/png"),
			attributionEvent("delta.text_append", "current-message", "foreign-device", map[string]any{"text": "FOREIGN_TEXT"}),
			attributionDone("current-message", "foreign-device", "FOREIGN_TEXT"),
			attributionMedia("current-message", node, "current.png", "image/png"),
			attributionDone("current-message", node, "BRIDGE_DIAG_OK"),
		}
	}
	result, events := runAttributionGeneration(t, m, "muse-spark")
	if result.Text != "BRIDGE_DIAG_OK" {
		t.Errorf("foreign device changed the current reply: %q", result.Text)
	}
	assertCurrentAttachment(t, result, events, "current.png")
}

func TestGenerateRejectsMediaWithoutDeviceOrMessageID(t *testing.T) {
	for _, name := range []string{"delta.presentation", "delta.message_done", "delta.tool_done", "delta.placeholder"} {
		t.Run(name, func(t *testing.T) {
			m := newMockHatch(t)
			defer m.close()
			m.streamEvents = func(node string) []map[string]any {
				media := attributionMedia("", "", "unattributed.png", "image/png")
				media["event"] = name
				payload := object(media["payload"])
				delete(payload, "message_id")
				payload["status"] = "completed"
				return []map[string]any{
					attributionEvent("delta.text_append", "current-message", node, map[string]any{"text": "BRIDGE_DIAG_OK"}),
					media,
					attributionMedia("current-message", node, "current.png", "image/png"),
					attributionDone("current-message", node, "BRIDGE_DIAG_OK"),
				}
			}
			result, events := runAttributionGeneration(t, m, "muse-spark")
			if result.Text != "BRIDGE_DIAG_OK" {
				t.Errorf("unattributed media changed the current reply: %q", result.Text)
			}
			assertCurrentAttachment(t, result, events, "current.png")
		})
	}
}

func TestGenerateExcessHistoryDoesNotConsumeCurrentAttachmentLimit(t *testing.T) {
	m := newMockHatch(t)
	defer m.close()
	for i := 0; i < 300; i++ {
		m.subscribeEvents = append(m.subscribeEvents, attributionMedia(
			fmt.Sprintf("history-message-%d", i), "", fmt.Sprintf("history-%d.png", i), "image/png",
		))
	}
	// The last pending resource is current, but its device-bearing confirmation
	// arrives only after the history has exceeded the pending window.
	m.subscribeEvents = append(m.subscribeEvents, attributionMedia("current-message", "", "current.png", "image/png"))
	m.streamEvents = func(node string) []map[string]any {
		return []map[string]any{
			attributionEvent("delta.text_append", "current-message", node, map[string]any{"text": "BRIDGE_DIAG_OK"}),
			attributionDone("current-message", node, "BRIDGE_DIAG_OK"),
		}
	}
	result, events := runAttributionGeneration(t, m, "muse-spark")
	if result.Text != "BRIDGE_DIAG_OK" {
		t.Errorf("current reply after history overflow = %q", result.Text)
	}
	assertCurrentAttachment(t, result, events, "current.png")
}

func TestGenerateDeduplicatesAttributedMediaAcrossEvents(t *testing.T) {
	m := newMockHatch(t)
	defer m.close()
	m.streamEvents = func(node string) []map[string]any {
		events := []map[string]any{
			attributionEvent("delta.text_append", "current-message", node, map[string]any{"text": "BRIDGE_DIAG_OK"}),
		}
		for i, device := range []string{node, "", node} {
			media := attributionMedia("current-message", device, "current.png", "image/png")
			// Distinct event sequences must still deduplicate the resource path.
			media["seq"] = i + 1
			events = append(events, media)
		}
		done := attributionDone("current-message", node, "BRIDGE_DIAG_OK")
		object(done["payload"])["data"] = object(attributionMedia("current-message", node, "current.png", "image/png")["payload"])["data"]
		return append(events, done)
	}
	result, events := runAttributionGeneration(t, m, "muse-spark")
	if result.Text != "BRIDGE_DIAG_OK" {
		t.Errorf("current reply with duplicate media = %q", result.Text)
	}
	assertCurrentAttachment(t, result, events, "current.png")
}

func assertCurrentAttachment(t *testing.T, result GenerateResult, events []Event, filename string) {
	t.Helper()
	wantPath := "sandbox://workspace/output/" + filename
	if len(result.Attachments) != 1 || result.Attachments[0].Path != wantPath {
		t.Errorf("attachments = %#v, want only %s", result.Attachments, filename)
	}
	var attachments []Attachment
	for _, event := range events {
		if event.Attachment != nil {
			attachments = append(attachments, *event.Attachment)
		}
	}
	if len(attachments) != 1 || attachments[0].Path != wantPath {
		t.Errorf("attachment callbacks = %#v, want only %s", attachments, filename)
	}
}
