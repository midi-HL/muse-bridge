package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/admin"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func TestCookieImportThroughHTTPHandler(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	vault, err := secure.NewVault(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir(), vault)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service, err := core.New(context.Background(), db, vault, secure.NewRedactor(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	srv := New(Options{Auth: admin.NewAuth(db, ""), Keys: admin.NewKeys(db, secure.NewRedactor()), Backend: service})
	setup := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/admin/api/auth/setup", bytes.NewBufferString(`{"password":"synthetic-password-1234","password_repeat":"synthetic-password-1234"}`))
	setup.Header.Set("Origin", "http://127.0.0.1:8787")
	setup.Header.Set("Content-Type", "application/json")
	setupWriter := httptest.NewRecorder()
	srv.ServeHTTP(setupWriter, setup)
	if setupWriter.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s", setupWriter.Code, setupWriter.Body.String())
	}
	var setupResponse struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(setupWriter.Body.Bytes(), &setupResponse); err != nil || setupResponse.CSRF == "" {
		t.Fatal("setup did not return CSRF token")
	}
	sessionCookies := setupWriter.Result().Cookies()
	if len(sessionCookies) != 1 {
		t.Fatalf("session cookie count=%d", len(sessionCookies))
	}
	body, err := json.Marshal(map[string]any{
		"action": "accounts.import",
		"args": map[string]any{
			"text": "datr=synthetic;hatch_sess=synthetic-session;hatch_vml=synthetic-vml",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/admin/api/action", bytes.NewReader(body))
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", setupResponse.CSRF)
	req.AddCookie(sessionCookies[0])
	writer := httptest.NewRecorder()
	srv.ServeHTTP(writer, req)
	if writer.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", writer.Code, writer.Body.String())
	}
	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			Count int `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &result); err != nil || !result.OK || result.Data.Count != 1 {
		t.Fatalf("unexpected import response: %s", writer.Body.String())
	}
}
