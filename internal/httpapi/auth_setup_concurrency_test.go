package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/admin"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func TestConcurrentFirstSetupHasSingleAdministrator(t *testing.T) {
	vault, err := secure.NewVault(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir(), vault)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := admin.NewAuth(db, "")
	server := New(Options{Auth: auth, LoopbackOnly: true})
	const attempts = 4
	type result struct {
		password string
		recorder *httptest.ResponseRecorder
	}
	ready := make(chan struct{}, attempts)
	start := make(chan struct{})
	finished := make(chan result, attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			password := fmt.Sprintf("synthetic-initial-password-%d", i)
			body, _ := json.Marshal(map[string]string{"password": password, "password_repeat": password})
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/admin/api/auth/setup", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://127.0.0.1:8787")
			w := httptest.NewRecorder()
			ready <- struct{}{}
			<-start
			server.ServeHTTP(w, req)
			finished <- result{password, w}
		}(i)
	}
	for i := 0; i < attempts; i++ {
		<-ready
	}
	close(start)
	var successes []result
	for i := 0; i < attempts; i++ {
		r := <-finished
		if r.recorder.Code == http.StatusOK {
			successes = append(successes, r)
		} else if r.recorder.Code != http.StatusUnauthorized {
			t.Errorf("unexpected setup status: %d", r.recorder.Code)
		}
	}
	if len(successes) != 1 {
		t.Fatalf("concurrent first setup created %d administrators; want exactly one", len(successes))
	}
	winner := successes[0]
	if _, _, err := auth.Login(context.Background(), winner.password, ""); err != nil {
		t.Fatalf("winning initial password no longer logs in: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/admin/api/auth/status", nil)
	for _, cookie := range winner.recorder.Result().Cookies() {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatalf("winning initial session invalidated: %s", w.Body.String())
	}
}
