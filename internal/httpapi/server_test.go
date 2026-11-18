package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/admin"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

type testBackend struct {
	calls        int
	requests     []core.ChatRequest
	err          error
	filePath     string
	filePlatform string
	fileUserID   string
}

func (b *testBackend) State(context.Context) (map[string]any, error) {
	return map[string]any{"settings": map[string]any{"telegram.token": "synthetic-sensitive-value"}}, nil
}
func (b *testBackend) Action(context.Context, string, map[string]any) (any, error) {
	b.calls++
	return map[string]any{"saved": true}, b.err
}
func (b *testBackend) Chat(_ context.Context, r core.ChatRequest, emit func(core.ChatEvent) error) (core.ChatResult, error) {
	b.requests = append(b.requests, r)
	if b.err != nil {
		return core.ChatResult{}, b.err
	}
	if e := emit(core.ChatEvent{Type: "text", Text: "hello"}); e != nil {
		return core.ChatResult{}, e
	}
	return core.ChatResult{SessionID: r.SessionID, Text: "hello"}, nil
}
func (b *testBackend) OpenFile(_ context.Context, _ string, platform, userID string) (*os.File, core.File, error) {
	b.filePlatform, b.fileUserID = platform, userID
	f, err := os.Open(b.filePath)
	return f, core.File{Filename: "page.html", MIME: "text/html"}, err
}

type harness struct {
	s       *Server
	db      *store.Store
	backend *testBackend
	cookie  *http.Cookie
	csrf    string
	key     string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	v, e := secure.NewVault(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if e != nil {
		t.Fatal(e)
	}
	db, e := store.Open(t.TempDir(), v)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	a := admin.NewAuth(db, "")
	keys := admin.NewKeys(db, secure.NewRedactor())
	backend := &testBackend{}
	s := New(Options{Auth: a, Keys: keys, Backend: backend, LoopbackOnly: true})
	h := &harness{s: s, db: db, backend: backend}
	resp := h.call("POST", "/admin/api/auth/setup", `{"password":"synthetic-password-1234","password_repeat":"synthetic-password-1234"}`, "http://127.0.0.1:8787", false, "")
	if resp.Code != 200 {
		t.Fatalf("setup: %s", resp.Body)
	}
	h.cookie = resp.Result().Cookies()[0]
	var data map[string]any
	json.Unmarshal(resp.Body.Bytes(), &data)
	h.csrf = data["csrf_token"].(string)
	value, e := keys.Action(context.Background(), "keys.create", map[string]any{"name": "test"})
	if e != nil {
		t.Fatal(e)
	}
	h.key = value.(map[string]any)["key"].(string)
	return h
}
func (h *harness) call(method, path, body, origin string, session bool, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if session {
		req.AddCookie(h.cookie)
		req.Header.Set("X-CSRF-Token", h.csrf)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.s.ServeHTTP(w, req)
	return w
}
func TestAuthCookieCSRFBearerSeparation(t *testing.T) {
	h := newHarness(t)
	if !h.cookie.HttpOnly || h.cookie.SameSite != http.SameSiteStrictMode || h.cookie.Path != "/" {
		t.Fatal("unsafe session cookie")
	}
	for _, origin := range []string{"", "https://evil.invalid", "http://127.0.0.1:8787/path"} {
		w := h.call("POST", "/action", `{"action":"settings.update","args":{}}`, origin, true, "")
		if w.Code != 403 {
			t.Fatalf("origin %q: %d", origin, w.Code)
		}
	}
	if h.backend.calls != 0 {
		t.Fatal("forbidden request caused mutation")
	}
	w := h.call("POST", "/action", `{"action":"settings.update","args":{}}`, "http://127.0.0.1:8787", true, "")
	if w.Code != 200 || h.backend.calls != 1 {
		t.Fatal("valid action failed")
	}
	if w = h.call("GET", "/state", "", "", false, h.cookie.Value); w.Code != 401 {
		t.Fatal("admin bearer accepted")
	}
	if w = h.call("GET", "/v1/models", "", "", true, ""); w.Code != 401 {
		t.Fatal("cookie accepted as API key")
	}
	if w = h.call("GET", "/v1/models", "", "", false, h.cookie.Value); w.Code != 401 {
		t.Fatal("console token accepted as API key")
	}
	if w = h.call("GET", "/v1/models", "", "", false, h.key); w.Code != 200 {
		t.Fatal("API key rejected")
	}
	if w = h.call("GET", "/state", "", "", true, ""); strings.Contains(w.Body.String(), "synthetic-sensitive-value") {
		t.Fatal("state leaked secret")
	}
	if w = h.call("POST", "/admin/api/auth/logout", `{}`, "http://127.0.0.1:8787", true, ""); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w = h.call("GET", "/state", "", "", true, ""); w.Code != 401 {
		t.Fatal("logout did not revoke persisted session")
	}
}
func TestWrongCSRFAndDNSRebinding(t *testing.T) {
	h := newHarness(t)
	h.csrf = "wrong"
	w := h.call("POST", "/action", `{"action":"settings.update"}`, "http://127.0.0.1:8787", true, "")
	if w.Code != 403 || h.backend.calls != 0 {
		t.Fatal("wrong CSRF accepted")
	}
	req := httptest.NewRequest("GET", "http://rebound.invalid:8787/admin/api/auth/status", nil)
	w = httptest.NewRecorder()
	h.s.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("DNS rebinding host accepted")
	}
}
func TestAuthRateLimitAndSafeErrors(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 10; i++ {
		w := h.call("POST", "/admin/api/auth/login", `{"password":"wrong"}`, "http://127.0.0.1:8787", false, "")
		if i == 9 && w.Code != 429 {
			t.Fatalf("rate limit absent: %d", w.Code)
		}
	}
	h.backend.err = errors.New("synthetic-cookie-value upstream-secret")
	w := h.call("POST", "/action", `{"action":"accounts.probe"}`, "http://127.0.0.1:8787", true, "")
	if strings.Contains(w.Body.String(), "synthetic-cookie") || strings.Contains(w.Body.String(), "upstream-secret") {
		t.Fatal("raw error leaked")
	}
	w = h.call("POST", "/action", `{"action":"not.registered"}`, "http://127.0.0.1:8787", true, "")
	if w.Code != 501 || !strings.Contains(w.Body.String(), "unsupported") {
		t.Fatal("unknown action pretended success")
	}
}
func TestOpenAIStreamingAndNonStreaming(t *testing.T) {
	h := newHarness(t)
	body := `{"model":"muse-spark","messages":[{"role":"user","content":"hi"}]}`
	w := h.call("POST", "/v1/chat/completions", body, "", false, h.key)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var result map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result["object"] != "chat.completion" {
		t.Fatal("invalid completion")
	}
	w = h.call("POST", "/v1/chat/completions", strings.TrimSuffix(body, "}")+`,"stream":true}`, "", false, h.key)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("not SSE")
	}
	for _, want := range []string{`"object":"chat.completion.chunk"`, `"content":"hello"`, `"finish_reason":"stop"`, `data: [DONE]`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
	w = h.call("POST", "/api/chat", `{"prompt":"hi","platform":"web","user_id":"admin","session_id":"victim"}`, "", false, h.key)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	request := h.backend.requests[len(h.backend.requests)-1]
	if request.Platform != "api" || request.UserID == "admin" || request.SessionID == "victim" {
		t.Fatal("API identity spoofing allowed")
	}
	if apiSession(request.UserID, request.SessionID) != request.SessionID {
		t.Fatal("API session cannot be resumed")
	}
	if apiSession("other-key", request.SessionID) == request.SessionID {
		t.Fatal("session isolation failed")
	}
}
func TestFileHandlerPassesAuthenticatedKeyIdentity(t *testing.T) {
	h := newHarness(t)
	h.backend.filePath = filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(h.backend.filePath, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := h.s.keys.Authenticate(context.Background(), h.key, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/files/syntheticID", "/admin/api/files/syntheticID"} {
		w := h.call("GET", route, "", "", false, h.key)
		if w.Code != 200 || h.backend.filePlatform != "api" || h.backend.fileUserID != key.ID {
			t.Fatal("file handler did not pass API ownership")
		}
		w = h.call("GET", route, "", "", true, "")
		if w.Code != 200 || h.backend.filePlatform != "" || h.backend.fileUserID != "" {
			t.Fatal("administrator file access changed")
		}
	}
}

func TestFileRequiresAuthAndForcesDownload(t *testing.T) {
	h := newHarness(t)
	h.backend.filePath = filepath.Join(t.TempDir(), "test")
	if os.WriteFile(h.backend.filePath, []byte("<script>unsafe</script>"), 0600) != nil {
		t.Fatal("write file")
	}
	w := h.call("GET", "/files/syntheticID", "", "", false, "")
	if w.Code != 401 {
		t.Fatal("unauthenticated file")
	}
	w = h.call("GET", "/files/syntheticID", "", "", true, "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("unsafe file delivery")
	}
	body, _ := io.ReadAll(w.Result().Body)
	if len(body) == 0 {
		t.Fatal("empty file")
	}
}
