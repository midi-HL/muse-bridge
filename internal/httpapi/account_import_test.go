package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/pool"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

func TestHTTPAccountCookieRefreshPreservesRouting(t *testing.T) {
	h := newHarness(t)
	vault, err := secure.NewVault(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := core.New(context.Background(), h.db, vault, secure.NewRedactor(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	h.s.backend = svc
	call := func(name string, args map[string]any) {
		t.Helper()
		b, err := json.Marshal(map[string]any{"action": name, "args": args})
		if err != nil {
			t.Fatal(err)
		}
		w := h.call("POST", "/admin/api/action", string(b), "http://127.0.0.1:8787", true, "")
		if w.Code != 200 {
			t.Fatalf("%s returned %d", name, w.Code)
		}
	}
	call("nodes.save", map[string]any{"id": "node", "name": "Synthetic node", "url": "http://127.0.0.1:18080", "enabled": true})
	call("accounts.import", map[string]any{"text": `{"id":"account","pool":"api","node_id":"node","note":"preserve","enabled":false,"cookies":{"hatch_sess":"synthetic-old"}}`})
	call("accounts.import", map[string]any{"text": `{"id":"account","cookies":{"hatch_sess":"synthetic-new"}}`})
	var got pool.Account
	if err = h.db.Get(context.Background(), "account", "account", &got); err != nil {
		t.Fatal(err)
	}
	if got.Pool != "api" || got.NodeID != "node" || got.Note != "preserve" || got.Enabled {
		t.Fatal("HTTP refresh lost routing or enabled disabled account")
	}
	var secret map[string]string
	if err = h.db.Secret(context.Background(), "account", "account", &secret); err != nil || secret["hatch_sess"] != "synthetic-new" {
		t.Fatal("encrypted cookie was not refreshed")
	}
}
