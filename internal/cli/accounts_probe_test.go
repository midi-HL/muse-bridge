package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountsProbeOptionalID(t *testing.T) {
	for _, tc := range []struct {
		name, id, action string
	}{
		{name: "all accounts", action: "protocol.probe"},
		{name: "single account", id: "account-one", action: "accounts.probe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var action string
			var accountID string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/admin/api/auth/status" {
					io.WriteString(w, `{"authenticated":true,"csrf_token":"synthetic-csrf"}`)
					return
				}
				var body struct {
					Action string            `json:"action"`
					Args   map[string]string `json:"args"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				action, accountID = body.Action, body.Args["id"]
				io.WriteString(w, `{"ok":true,"data":{"checked":true}}`)
			}))
			defer ts.Close()
			r := Runner{Out: io.Discard, Env: func(k string) string {
				if k == "MUSE_ADMIN_COOKIE" {
					return "synthetic-session"
				}
				return ""
			}}
			args := []string{"--url", ts.URL, "accounts", "probe"}
			if tc.id != "" {
				args = append(args, tc.id)
			}
			if err := r.Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			if action != tc.action || accountID != tc.id {
				t.Fatalf("probe dispatched %q id=%q, want %q id=%q", action, accountID, tc.action, tc.id)
			}
		})
	}
}
