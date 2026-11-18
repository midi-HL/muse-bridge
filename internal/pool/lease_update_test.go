package pool

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func TestActiveBootstrapSurvivesMetadataRoutingAndCookieUpdates(t *testing.T) {
	for _, change := range []string{"note", "node", "cookie"} {
		t.Run(change, func(t *testing.T) {
			db := routingTestStore(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var oldHits, newHits atomic.Int32
			oldProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				oldHits.Add(1)
				if r.Header.Get("Cookie") != "hatch_sess=old" && change == "cookie" {
					t.Error("old request credential changed")
				}
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer oldProxy.Close()
			newProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { newHits.Add(1); w.WriteHeader(http.StatusUnauthorized) }))
			defer newProxy.Close()
			addTestNode(t, db, "old", oldProxy.URL)
			addTestNode(t, db, "new", newProxy.URL)
			p, err := New(db, hatch.Config{BaseURL: "http://muse.invalid"})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if err = p.Add(context.Background(), Account{ID: "active", Enabled: true, NodeID: "old"}, map[string]string{"hatch_sess": "old"}); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_ = p.WithClient(context.Background(), func(c *hatch.Client, _ Account) error {
					_, probeErr := c.Probe(context.Background())
					result <- probeErr
					return nil
				})
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("bootstrap did not start")
			}
			switch change {
			case "note":
				_, err = p.Update(context.Background(), "active", map[string]any{"note": "changed"})
			case "node":
				_, err = p.Update(context.Background(), "active", map[string]any{"node_id": "new"})
			case "cookie":
				_, err = p.Import(context.Background(), "active", nil, map[string]string{"hatch_sess": "new"})
			}
			if err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case probeErr := <-result:
				var he *hatch.Error
				if !errors.As(probeErr, &he) || he.Kind != "auth" {
					t.Fatalf("update interrupted in-flight bootstrap instead of receiving proxy response: %v", probeErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("request did not finish")
			}
			// Wait for the lease owner to finish before checking the next lease.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if change == "node" {
				_ = p.WithClient(ctx, func(c *hatch.Client, _ Account) error { _, e := c.Probe(ctx); return e })
				if oldHits.Load() != 1 || newHits.Load() != 1 {
					t.Fatal("next lease did not switch to new node")
				}
			}
			if change == "cookie" {
				var cookies map[string]string
				if err = db.Secret(ctx, "account", "active", &cookies); err != nil || cookies["hatch_sess"] != "new" {
					t.Fatal("old lease overwrote imported credentials")
				}
			}
		})
	}
}
