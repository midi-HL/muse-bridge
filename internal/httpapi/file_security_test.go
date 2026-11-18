package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

func TestRealFileBackendRejectsCrossKeyAndExternalPaths(t *testing.T) {
	h := newHarness(t)
	vault, err := secure.NewVault(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	svc, err := core.New(context.Background(), h.db, vault, secure.NewRedactor(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	h.s.backend = svc
	owner, err := h.s.keys.Authenticate(context.Background(), h.key, false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := h.s.keys.Action(context.Background(), "keys.create", map[string]any{"name": "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := created.(map[string]any)["key"].(string)
	generated := filepath.Join(dir, "files", "owned.bin")
	if err = os.WriteFile(generated, []byte("generated sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err = os.WriteFile(outside, []byte("external sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, path string }{{"owned", "owned.bin"}, {"outside", outside}} {
		err = h.db.Put(context.Background(), "file", item.id, map[string]any{"id": item.id, "filename": "../download.html", "mime": "text/html", "path": item.path, "platform": "api", "user_id": owner.ID}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		id, key string
		admin   bool
		want    int
	}{{"owned", h.key, false, 200}, {"owned", foreign, false, 404}, {"owned", "", true, 200}, {"outside", h.key, false, 404}, {"outside", "", true, 404}} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := h.call(method, "/files/"+test.id, "", "", test.admin, test.key)
			if w.Code != test.want {
				t.Fatalf("%s %s status %d expected %d", method, test.id, w.Code, test.want)
			}
			if strings.Contains(w.Body.String(), "external sentinel") || strings.Contains(w.Body.String(), outside) {
				t.Fatal("external file or host path leaked")
			}
			if w.Code == 200 && method == http.MethodGet && w.Body.String() != "generated sentinel" {
				t.Fatal("authorized content not delivered")
			}
		}
	}
}
