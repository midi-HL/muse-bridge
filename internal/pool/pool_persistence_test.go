package pool

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func TestDisabledAccountRemainsDisabledAcrossReload(t *testing.T) {
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
	ctx := context.Background()
	p, err := New(db, hatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(ctx, Account{ID: "disabled-1", Label: "disabled", Status: "disabled", Enabled: false}, map[string]string{"hatch_sess": "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if got := p.List()[0]; got.Enabled {
		t.Fatal("disabled account was enabled on add")
	}
	p2, err := New(db, hatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := p2.List()[0]; got.Enabled || got.Status != "disabled" {
		t.Fatalf("disabled account changed after reload: %#v", got)
	}
}
