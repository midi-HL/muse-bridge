package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestChatRoutesChargeAPIKeyLimitsAfterValidation(t *testing.T) {
	routes := []struct {
		name  string
		path  string
		bad   string
		valid string
	}{
		{
			name:  "simple chat",
			path:  "/api/chat",
			bad:   `{"prompt":"valid","attachments":[{"filename":"` + strings.Repeat("x", 1025) + `","mime":"text/plain","data_base64":"YQ=="}]}`,
			valid: `{"prompt":"valid"}`,
		},
		{
			name:  "simple stream",
			path:  "/api/chat/stream",
			bad:   `{"prompt":"valid","attachments":[{"filename":"` + strings.Repeat("x", 1025) + `","mime":"text/plain","data_base64":"YQ=="}]}`,
			valid: `{"prompt":"valid"}`,
		},
		{
			name:  "OpenAI completions",
			path:  "/v1/chat/completions",
			bad:   `{"model":"unknown","messages":[{"role":"user","content":"invalid"}]}`,
			valid: `{"model":"muse-spark","messages":[{"role":"user","content":"valid"}]}`,
		},
	}

	for _, route := range routes {
		for _, limit := range []struct {
			name  string
			rpm   float64
			daily float64
		}{
			{name: "RPM", rpm: 1},
			{name: "daily", daily: 1},
		} {
			t.Run(route.name+"/"+limit.name, func(t *testing.T) {
				h := newHarness(t)
				created, err := h.keys.Action(context.Background(), "keys.create", map[string]any{
					"name":  "limited",
					"rpm":   limit.rpm,
					"daily": limit.daily,
				})
				if err != nil {
					t.Fatal(err)
				}
				data := created.(map[string]any)
				token := data["key"].(string)
				id := data["id"].(string)
				fixedNow := time.Date(2026, 11, 19, 4, 12, 30, 0, time.UTC)
				h.keys.SetClockForTesting(func() time.Time { return fixedNow })

				invalid := h.call(http.MethodPost, route.path, route.bad, "", false, token)
				if invalid.Code != http.StatusBadRequest {
					t.Fatalf("invalid request status=%d body=%s", invalid.Code, invalid.Body.String())
				}
				assertAPIKeyCounters(t, h, id, fixedNow, 0, 0, 0)

				first := h.call(http.MethodPost, route.path, route.valid, "", false, token)
				if first.Code != http.StatusOK {
					t.Fatalf("first valid request status=%d body=%s", first.Code, first.Body.String())
				}
				assertAPIKeyCounters(t, h, id, fixedNow, 1, 1, 1)

				second := h.call(http.MethodPost, route.path, route.valid, "", false, token)
				if second.Code != http.StatusTooManyRequests || errorCode(t, second) != "rate_limit" {
					t.Fatalf("request beyond API-key %s limit was accepted: status=%d body=%s", limit.name, second.Code, second.Body.String())
				}
				wantMinute := 1
				if limit.name == "daily" {
					// Charge increments RPM before the daily counter refuses.
					wantMinute = 2
				}
				assertAPIKeyCounters(t, h, id, fixedNow, wantMinute, 1, 1)
			})
		}
	}
}

func assertAPIKeyCounters(t *testing.T, h *harness, id string, now time.Time, wantMinute, wantDaily, wantTotal int) {
	t.Helper()
	var itemFound bool
	items, err := h.keys.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == id {
			itemFound = true
			if item.TodayUsed != wantDaily || item.TotalUsed != wantTotal {
				t.Fatalf("API key daily/total counters = %d/%d, want %d/%d", item.TodayUsed, item.TotalUsed, wantDaily, wantTotal)
			}
		}
	}
	if !itemFound {
		t.Fatalf("API key %q was not present in key list", id)
	}
	minute := now.UTC().Format("2006-01-02T15:04")
	if got := h.db.Count(context.Background(), "api_minute", id, minute); got != wantMinute {
		t.Fatalf("API key minute counter = %d, want %d", got, wantMinute)
	}
}
