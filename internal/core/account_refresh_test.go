package core

import (
	"context"
	"testing"
)

func TestAccountImportExplicitRoutingAndInvalidMetadata(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if err := db.Put(ctx, "node", "n", map[string]any{"id": "n", "enabled": true}, map[string]string{"url": "http://127.0.0.1:18080"}); err != nil {
		t.Fatal(err)
	}
	initial := `{"id":"a","pool":"api","note":"retained","node_id":"n","enabled":false,"cookies":{"hatch_sess":"old"}}`
	if _, err := s.Action(ctx, "accounts.import", map[string]any{"text": initial}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "accounts.import", map[string]any{"id": "a", "cookies": map[string]string{"hatch_sess": "new"}}); err != nil {
		t.Fatal(err)
	}
	got := s.pool.List()[0]
	if got.Enabled || got.NodeID != "n" || got.Pool != "api" || got.Note != "retained" {
		t.Fatal("structured cookie refresh reset metadata")
	}
	before := got
	for _, bad := range []string{`{"id":"a","pool":"invalid","cookies":{"hatch_sess":"bad"}}`, `{"id":"a","node_id":"missing","cookies":{"hatch_sess":"bad"}}`, `{"id":"a","enabled":"yes","cookies":{"hatch_sess":"bad"}}`, `{"id":false,"cookies":{"hatch_sess":"bad"}}`} {
		if _, err := s.Action(ctx, "accounts.import", map[string]any{"text": bad}); err == nil {
			t.Error("invalid import succeeded")
		}
		if s.pool.List()[0] != before {
			t.Error("failed import mutated account")
		}
	}
	var secret map[string]string
	if err := db.Secret(ctx, "account", "a", &secret); err != nil || secret["hatch_sess"] != "new" {
		t.Fatal("failed import changed encrypted cookie")
	}
	if _, err := s.Action(ctx, "accounts.import", map[string]any{"text": `{"id":"a","pool":"chat","node_id":"","note":"","enabled":true,"cookies":{"hatch_sess":"third"}}`}); err != nil {
		t.Fatal(err)
	}
	got = s.pool.List()[0]
	if !got.Enabled || got.NodeID != "" || got.Pool != "chat" || got.Note != "" {
		t.Fatal("explicit import metadata was ignored")
	}
}
