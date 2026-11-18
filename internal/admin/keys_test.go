package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
	"testing"
)

func TestKeyUpdatePreservesUnspecifiedLimit(t *testing.T) {
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		t.Fatal(e)
	}
	v, e := secure.NewVault(base64.StdEncoding.EncodeToString(key))
	if e != nil {
		t.Fatal(e)
	}
	db, e := store.Open(t.TempDir(), v)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	r := secure.NewRedactor()
	keys := NewKeys(db, r)
	ctx := context.Background()
	created, e := keys.Action(ctx, "keys.create", map[string]any{"name": "test", "rpm": float64(10), "daily": float64(20)})
	if e != nil {
		t.Fatal(e)
	}
	id := created.(map[string]any)["id"].(string)
	if _, e = keys.Action(ctx, "keys.update", map[string]any{"id": id, "rpm": float64(30)}); e != nil {
		t.Fatal(e)
	}
	var item APIKey
	if e = db.Get(ctx, "api_key", id, &item); e != nil {
		t.Fatal(e)
	}
	if item.RPM != 30 || item.Daily != 20 {
		t.Fatalf("partial update reset limit: %#v", item)
	}
}
