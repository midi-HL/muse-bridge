package pool

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func routingTestStore(t *testing.T) *store.Store {
	t.Helper()
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
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type routedPool interface {
	WithClientFor(context.Context, string, bool, func(*hatch.Client, Account) error) error
}

func withRoutedClient(t *testing.T, p *Pool, ctx context.Context, platform string, split bool, fn func(*hatch.Client, Account) error) error {
	t.Helper()
	rp, ok := any(p).(routedPool)
	if !ok {
		t.Fatal("pool does not expose WithClientFor")
	}
	return rp.WithClientFor(ctx, platform, split, fn)
}

func publicAccount(t *testing.T, a Account) map[string]any {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func addTestNode(t *testing.T, db *store.Store, id, proxyURL string) {
	t.Helper()
	ctx := context.Background()
	if err := db.Put(ctx, "node", id, map[string]any{"id": id, "enabled": true}, map[string]string{"url": proxyURL}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePersistsRoutingFieldsAndRejectsInvalidInputAtomically(t *testing.T) {
	db := routingTestStore(t)
	p, err := New(db, hatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := p.Add(ctx, Account{ID: "a", Label: "before", Enabled: true}, map[string]string{"hatch_sess": "synthetic"}); err != nil {
		t.Fatal(err)
	}
	addTestNode(t, db, "node-a", "http://127.0.0.1:8888")
	got, err := p.Update(ctx, "a", map[string]any{"id": "a", "label": "after", "node_id": "node-a", "pool": "api", "note": "private note"})
	if err != nil {
		t.Fatal(err)
	}
	public := publicAccount(t, got)
	for key, want := range map[string]any{"label": "after", "node_id": "node-a", "pool": "api", "note": "private note"} {
		if public[key] != want {
			t.Errorf("%s = %#v, want %#v", key, public[key], want)
		}
	}
	before := p.List()[0]
	if _, err := p.Update(ctx, "a", map[string]any{"pool": "other"}); err == nil {
		t.Fatal("invalid pool was accepted")
	}
	if _, err := p.Update(ctx, "a", map[string]any{"enabled": "yes"}); err == nil {
		t.Fatal("invalid enabled type was accepted")
	}
	if _, err := p.Update(ctx, "a", map[string]any{"unknown": true}); err == nil {
		t.Fatal("unknown update field was accepted")
	}
	if _, err := p.Update(ctx, "a", map[string]any{"id": "other"}); err == nil {
		t.Fatal("mismatched account id was accepted")
	}
	if after := p.List()[0]; after != before {
		t.Fatalf("invalid update mutated account: before=%#v after=%#v", before, after)
	}
	p2, err := New(db, hatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if after := publicAccount(t, p2.List()[0]); after["node_id"] != "node-a" || after["pool"] != "api" || after["note"] != "private note" {
		t.Fatalf("routing fields did not survive reload: %#v", after)
	}
}

func TestProbePreservesPendingWorkspaceAndAuthenticationClassification(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
		body       string
		wantStatus string
		wantError  string
	}{
		{name: "workspace pending", statusCode: http.StatusOK, body: "{\"status\":\"unassigned\"}", wantStatus: "pending_vm", wantError: "workspaceNotRunning"},
		{name: "cookie authentication rejected", statusCode: http.StatusUnauthorized, body: "{}", wantStatus: "invalid", wantError: "upstreamAuthFailed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/session" {
					t.Errorf("unexpected probe route %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			p, err := New(routingTestStore(t), hatch.Config{BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if err := p.Add(context.Background(), Account{ID: "classified", Enabled: true}, map[string]string{"hatch_sess": "synthetic-session"}); err != nil {
				t.Fatal(err)
			}
			got, probeErr := p.Probe(context.Background(), "classified")
			if probeErr == nil {
				t.Fatal("expected probe failure")
			}
			if got.Status != test.wantStatus || got.LastError != test.wantError {
				t.Fatalf("status=%q last_error=%q; want %q/%q", got.Status, got.LastError, test.wantStatus, test.wantError)
			}
			if persisted := p.List()[0]; persisted.Status != test.wantStatus || persisted.LastError != test.wantError {
				t.Fatalf("classification was not persisted: %#v", persisted)
			}
		})
	}
}

func TestPoolRoutingAndLegacyPoolDefault(t *testing.T) {
	db := routingTestStore(t)
	p, err := New(db, hatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, a := range []Account{
		{ID: "api", Pool: "api", Enabled: true},
		{ID: "chat", Pool: "chat", Enabled: true},
		{ID: "shared", Enabled: true},
	} {
		if err := p.Add(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
	}
	legacy := map[string]any{"id": "legacy", "enabled": true, "status": "available"}
	if err := db.Put(ctx, "account", "legacy", legacy, nil); err != nil {
		t.Fatal(err)
	}
	p2, err := New(db, hatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := publicAccount(t, accountByID(t, p2.List(), "legacy"))["pool"]; got != "shared" {
		t.Fatalf("legacy pool = %#v, want shared", got)
	}
	for platform, allowed := range map[string]map[string]bool{
		"api":      {"api": true, "shared": true, "legacy": true},
		"web":      {"chat": true, "shared": true, "legacy": true},
		"telegram": {"chat": true, "shared": true, "legacy": true},
	} {
		seen := map[string]bool{}
		for i := 0; i < 12; i++ {
			if err := withRoutedClient(t, p2, ctx, platform, true, func(_ *hatch.Client, a Account) error {
				if !allowed[a.ID] {
					t.Errorf("%s selected wrong pool account %q", platform, a.ID)
				}
				seen[a.ID] = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		if !seen["shared"] {
			t.Errorf("%s never selected shared account", platform)
		}
	}
	seenAll := map[string]bool{}
	for i := 0; i < 24; i++ {
		if err := withRoutedClient(t, p2, ctx, "api", false, func(_ *hatch.Client, a Account) error {
			seenAll[a.ID] = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seenAll) != 4 {
		t.Fatalf("split=false should select every healthy account, got %v", seenAll)
	}
}

func accountByID(t *testing.T, accounts []Account, id string) Account {
	t.Helper()
	for _, a := range accounts {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("account %q not found", id)
	return Account{}
}

func TestSelectedNodeCarriesRealBootstrapTrafficAfterReload(t *testing.T) {
	db := routingTestStore(t)
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.URL.Path != "/api/session" {
			t.Errorf("unexpected proxy request path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer proxy.Close()
	addTestNode(t, db, "proxy-node", proxy.URL)
	p, err := New(db, hatch.Config{BaseURL: "http://muse.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(context.Background(), Account{ID: "routed", Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Update(context.Background(), "routed", map[string]any{"node_id": "proxy-node", "pool": "api"}); err != nil {
		t.Fatal(err)
	}
	p2, err := New(db, hatch.Config{BaseURL: "http://muse.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	err = withRoutedClient(t, p2, context.Background(), "api", true, func(c *hatch.Client, _ Account) error {
		_, err := c.Probe(context.Background())
		return err
	})
	if err == nil {
		t.Fatal("synthetic proxy should reject bootstrap")
	}
	if got := proxyHits.Load(); got != 1 {
		t.Fatalf("bootstrap proxy hits = %d, want 1", got)
	}
}

func TestDisabledBoundNodeCanBeUnboundOrAccountDisabled(t *testing.T) {
	for _, action := range []string{"unbind", "disable"} {
		t.Run(action, func(t *testing.T) {
			db := routingTestStore(t)
			addTestNode(t, db, "node", "http://127.0.0.1:8080")
			p, err := New(db, hatch.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Add(context.Background(), Account{ID: "bound", Enabled: true, NodeID: "node"}, nil); err != nil {
				t.Fatal(err)
			}
			if err := db.Put(context.Background(), "node", "node", map[string]any{"id": "node", "enabled": false}, map[string]string{"url": "http://127.0.0.1:8080"}); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"id": "bound"}
			if action == "unbind" {
				args["node_id"] = ""
			} else {
				args["enabled"] = false
			}
			got, err := p.Update(context.Background(), "bound", args)
			if err != nil {
				t.Fatal(err)
			}
			if action == "unbind" && got.NodeID != "" {
				t.Fatalf("node binding remains: %#v", got)
			}
			if action == "disable" && got.Enabled {
				t.Fatalf("account remains enabled: %#v", got)
			}
			if action == "disable" {
				if err := p.Remove(context.Background(), "bound"); err != nil {
					t.Fatalf("disabled-node account could not be deleted: %v", err)
				}
			}
		})
	}
}

func TestPoolCloseRejectsNewLeasesAndProbe(t *testing.T) {
	db := routingTestStore(t)
	p, err := New(db, hatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(context.Background(), Account{ID: "a", Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := withRoutedClient(t, p, context.Background(), "api", false, func(*hatch.Client, Account) error { return nil }); err == nil {
		t.Fatal("closed pool accepted a lease")
	}
	if _, err := p.Probe(context.Background(), "a"); err == nil {
		t.Fatal("closed pool accepted Probe")
	}
}

func TestNodeURLChangeUsesNewProxyOnNextLease(t *testing.T) {
	db := routingTestStore(t)
	var firstHits, secondHits atomic.Int32
	newProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer newProxy.Close()
	oldProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer oldProxy.Close()
	addTestNode(t, db, "node", oldProxy.URL)
	p, err := New(db, hatch.Config{BaseURL: "http://muse.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(context.Background(), Account{ID: "bound", Enabled: true, NodeID: "node"}, nil); err != nil {
		t.Fatal(err)
	}
	probe := func() {
		t.Helper()
		_ = p.WithClient(context.Background(), func(c *hatch.Client, _ Account) error { _, err := c.Probe(context.Background()); return err })
	}
	probe()
	if firstHits.Load() != 1 {
		t.Fatalf("old proxy hits = %d, want 1", firstHits.Load())
	}
	if err := db.Put(context.Background(), "node", "node", map[string]any{"id": "node", "enabled": true}, map[string]string{"url": newProxy.URL}); err != nil {
		t.Fatal(err)
	}
	probe()
	if secondHits.Load() != 1 {
		t.Fatalf("new proxy hits = %d, want 1", secondHits.Load())
	}
	if firstHits.Load() != 1 {
		t.Fatalf("stale proxy was reused after node update: %d hits", firstHits.Load())
	}
}

func TestBusyUpdateAndRemoveCannotBeOverwrittenByOldLease(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "remove"}[remove], func(t *testing.T) {
			db := routingTestStore(t)
			p, err := New(db, hatch.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Add(context.Background(), Account{ID: "busy", Enabled: true, Pool: "api"}, nil); err != nil {
				t.Fatal(err)
			}
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				finished <- withRoutedClient(t, p, context.Background(), "api", true, func(_ *hatch.Client, _ Account) error {
					close(entered)
					<-release
					return errors.New("synthetic old request failure")
				})
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("lease did not start")
			}
			if remove {
				if err := p.Remove(context.Background(), "busy"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := p.Update(context.Background(), "busy", map[string]any{"label": "new", "pool": "chat", "note": "new metadata"}); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("lease did not finish")
			}
			if remove {
				if p.Count() != 0 {
					t.Fatalf("deleted account was revived: %#v", p.List())
				}
				return
			}
			got := accountByID(t, p.List(), "busy")
			public := publicAccount(t, got)
			if public["label"] != "new" || public["pool"] != "chat" || public["note"] != "new metadata" {
				t.Fatalf("old lease overwrote newer account config: %#v", public)
			}
		})
	}
}

func TestBusyWaitCancellationDoesNotMarkAccountError(t *testing.T) {
	db := routingTestStore(t)
	p, err := New(db, hatch.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(context.Background(), Account{ID: "busy", Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- p.WithClient(context.Background(), func(_ *hatch.Client, _ Account) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.WithClient(ctx, func(*hatch.Client, Account) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled busy wait error = %v", err)
	}
	close(release)
	<-finished
	if got := p.List()[0]; got.Status == "error" || got.LastError != "" {
		t.Fatalf("cancelled waiter marked account error: %#v", got)
	}
}
