package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

func newChatLimitsEntryHarness(t *testing.T) (*harness, *core.Service) {
	t.Helper()
	h := newHarness(t)
	vault, err := secure.NewVault(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	service, err := core.New(context.Background(), h.db, vault, secure.NewRedactor(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if _, err = service.Action(context.Background(), "accounts.import", map[string]any{"id": "synthetic", "cookies": map[string]string{"hatch_sess": "synthetic-only"}}); err != nil {
		t.Fatal(err)
	}
	h.s.backend = service
	return h, service
}

func TestChatLimitsEntryIdentitySpoofAndSharedAPIKeyQuota(t *testing.T) {
	h, service := newChatLimitsEntryHarness(t)
	ctx := context.Background()
	if _, err := service.Action(ctx, "settings.update", map[string]any{"limits.daily_per_user": 1}); err != nil {
		t.Fatal(err)
	}

	var generated []string
	service.SetGenerateForTest(func(_ context.Context, _ *hatch.Client, req hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
		generated = append(generated, req.Prompt)
		return hatch.GenerateResult{Text: "ok"}, nil
	})

	chatBody := `{"prompt":"admin accepted","session_id":"web-session","platform":"forged-platform","user_id":"forged-user","retry_user_id":"forged-retry"}`
	first := h.call(http.MethodPost, "/admin/api/chat", chatBody, "http://127.0.0.1:8787", true, "")
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"type":"done"`) {
		t.Fatalf("admin web chat failed through authenticated entry point: status=%d body=%s", first.Code, first.Body.String())
	}
	limitedWeb := h.call(http.MethodPost, "/admin/api/chat", `{"prompt":"admin over quota","session_id":"web-limited","platform":"forged-platform","user_id":"another-user","retry_user_id":"another-retry"}`, "http://127.0.0.1:8787", true, "")
	assertSSEErrorCode(t, "web admin daily limit", limitedWeb, "daily_limit")
	if limitedWeb.Code != http.StatusOK {
		t.Fatalf("web chat SSE error should retain HTTP 200 safe-response format, got %d", limitedWeb.Code)
	}

	// A single authenticated API key is the quota owner across both direct API
	// endpoints and OpenAI-compatible completions; client identity fields are
	// intentionally varied and must not change that subject.
	// The Web admin unit belongs to platform/user web/admin, not to the API key
	// even though this request supplies those exact forged identity values.
	apiFirst := h.call(http.MethodPost, "/api/chat", `{"prompt":"key accepted","session_id":"api-one","platform":"web","user_id":"admin","retry_user_id":"spoof"}`, "", false, h.key)
	if apiFirst.Code != http.StatusOK {
		t.Fatalf("first request on API-key subject should be independent from Web admin: status=%d body=%s", apiFirst.Code, apiFirst.Body.String())
	}
	apiLimited := h.call(http.MethodPost, "/api/chat/stream", `{"prompt":"stream overage","session_id":"api-two","platform":"attacker","user_id":"attacker","retry_user_id":"attacker"}`, "", false, h.key)
	assertSSEErrorCode(t, "API stream shared daily quota", apiLimited, "daily_limit")
	if apiLimited.Code != http.StatusOK {
		t.Fatalf("/api/chat/stream error must be SSE HTTP 200, got %d", apiLimited.Code)
	}
	completionLimited := h.call(http.MethodPost, "/v1/chat/completions", `{"model":"muse-spark","session_id":"openai-third","messages":[{"role":"user","content":"completion overage"}],"stream":false}`, "", false, h.key)
	if completionLimited.Code != http.StatusTooManyRequests || errorCode(t, completionLimited) != "daily_limit" {
		t.Fatalf("OpenAI entry did not share API-key quota: status=%d body=%s", completionLimited.Code, completionLimited.Body.String())
	}

	// A different key, not a spoofed request-body user, is the independent API
	// principal and can still spend its own quota.
	other, err := h.keys.Action(ctx, "keys.create", map[string]any{"name": "independent"})
	if err != nil {
		t.Fatal(err)
	}
	otherKey := other.(map[string]any)["key"].(string)
	otherResult := h.call(http.MethodPost, "/api/chat", `{"prompt":"other key","platform":"api","user_id":"`+h.key+`","retry_user_id":"other"}`, "", false, otherKey)
	if otherResult.Code != http.StatusOK {
		t.Fatalf("different API key should have independent daily quota: status=%d body=%s", otherResult.Code, otherResult.Body.String())
	}
	if len(generated) != 3 {
		t.Fatalf("backend generation count=%d, want admin success plus two distinct-key successes; prompts=%q", len(generated), generated)
	}

	// Persisted records prove the actual Core request used trusted identities,
	// not the identity values supplied in each request body.
	var webIdentity struct {
		Platform string `json:"platform"`
		UserID   string `json:"user_id"`
	}
	if err := h.db.Get(ctx, "session", "web-session", &webIdentity); err != nil {
		t.Fatal(err)
	}
	if webIdentity.Platform != "web" || webIdentity.UserID != "admin" {
		t.Fatalf("admin chat trusted caller identity: platform=%q user=%q", webIdentity.Platform, webIdentity.UserID)
	}
	keyIdentity, err := h.keys.Authenticate(ctx, h.key, false)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity, err := h.keys.Authenticate(ctx, otherKey, false)
	if err != nil {
		t.Fatal(err)
	}
	wantAPIUsers := map[string]bool{keyIdentity.ID: true, otherIdentity.ID: true}
	var apiOwnedSessions int
	rows, err := h.db.List(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		var identity struct {
			Platform string `json:"platform"`
			UserID   string `json:"user_id"`
		}
		if err := json.Unmarshal(row.Data, &identity); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(row.ID, "api_") {
			apiOwnedSessions++
			if identity.Platform != "api" || !wantAPIUsers[identity.UserID] {
				t.Fatalf("API session identity accepted caller spoof or wrong key owner: id=%q platform=%q user=%q", row.ID, identity.Platform, identity.UserID)
			}
		}
	}
	if apiOwnedSessions != 2 {
		t.Fatalf("stored API session count=%d, want only the accepted sessions for two real keys", apiOwnedSessions)
	}
}

func TestChatLimitsEntryQueueFullAndAdminSSEErrorCodes(t *testing.T) {
	h, service := newChatLimitsEntryHarness(t)
	ctx := context.Background()
	if _, err := service.Action(ctx, "settings.update", map[string]any{"limits.concurrency": 1, "limits.queue_size": 0, "limits.daily_per_user": 10}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	service.SetGenerateForTest(func(run context.Context, _ *hatch.Client, _ hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-unblock:
			return hatch.GenerateResult{Text: "released"}, nil
		case <-run.Done():
			return hatch.GenerateResult{}, run.Err()
		}
	})
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- h.call(http.MethodPost, "/admin/api/chat", `{"prompt":"holds the sole slot","session_id":"queue-holder"}`, "http://127.0.0.1:8787", true, "")
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(unblock)
		t.Fatal("real authenticated admin chat did not reach the core generator")
	}
	queued := h.call(http.MethodPost, "/admin/api/chat", `{"prompt":"queue full","session_id":"queue-full"}`, "http://127.0.0.1:8787", true, "")
	assertSSEErrorCode(t, "web admin queue full", queued, "queue_full")
	if queued.Code != http.StatusOK {
		t.Fatalf("admin queue error should use HTTP 200 SSE envelope, got %d", queued.Code)
	}
	close(unblock)
	select {
	case result := <-firstDone:
		if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"type":"done"`) {
			t.Fatalf("queue holder result status=%d body=%s", result.Code, result.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queue holder did not finish after release")
	}
}

func assertSSEErrorCode(t *testing.T, label string, response *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%s did not preserve SSE content type: %q body=%s", label, response.Header().Get("Content-Type"), response.Body.String())
	}
	for _, line := range strings.Split(response.Body.String(), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("%s has malformed SSE data %q: %v", label, line, err)
		}
		if event.Type == "error" && event.Error.Code == wantCode {
			return
		}
	}
	t.Fatalf("%s missing explicit SSE error code %q: %s", label, wantCode, response.Body.String())
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("error response is not JSON: %s (%v)", response.Body.String(), err)
	}
	return payload.Error.Code
}
