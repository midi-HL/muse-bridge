package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFailedPrivateOutputCanBeRetried(t *testing.T) {
	for _, operation := range []string{"key", "backup"} {
		t.Run(operation, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/admin/api/auth/status" {
					io.WriteString(w, `{"authenticated":true,"csrf_token":"synthetic-csrf"}`)
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer ts.Close()
			r := Runner{Out: io.Discard, Env: func(k string) string {
				switch k {
				case "MUSE_ADMIN_COOKIE":
					return "synthetic-session"
				case "MUSE_BACKUP_PASSPHRASE":
					return "synthetic-backup-passphrase"
				}
				return ""
			}}
			path := filepath.Join(t.TempDir(), "output")
			args := []string{"--url", ts.URL}
			if operation == "key" {
				args = append(args, "keys", "create", "--name", "retry", "--out", path)
			} else {
				args = append(args, "backup", "export", "--file", path)
			}
			if err := r.Run(context.Background(), args); err == nil {
				t.Fatal("service failure returned success")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("failed %s export left a reserved file that blocks retries: %v", operation, err)
			}
		})
	}
}
