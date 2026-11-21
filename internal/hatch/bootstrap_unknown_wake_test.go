package hatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type unknownWakeFixture struct {
	client       *Client
	sessionCalls atomic.Int32
	wakeCalls    atomic.Int32
	tokenCalls   atomic.Int32
	wsCalls      atomic.Int32
}

// UNKNOWN is an absent control-plane observation both before and after wake.
// The gateway becomes reachable independently of that state label.
func newUnknownWakeFixture(t *testing.T, wakeStatus int, alreadyRunning bool) *unknownWakeFixture {
	t.Helper()
	f := &unknownWakeFixture{}
	m := newMockHatch(t)
	t.Cleanup(m.close)
	var running atomic.Bool
	running.Store(alreadyRunning)
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.wsCalls.Add(1)
		if !running.Load() {
			http.Error(w, "synthetic cold gateway", http.StatusBadGateway)
			return
		}
		m.wsHandler(w, r)
	}))
	t.Cleanup(ws.Close)
	bootstrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			f.sessionCalls.Add(1)
			writeJSON(w, map[string]any{
				"status": "assigned", "vm_state": "UNKNOWN",
				"vm_id": "vm-test", "endpoint_url": "https://vm.test", "vm_name": "vm",
			})
		case "/api/hatch/vm/wake":
			f.wakeCalls.Add(1)
			if r.Method != http.MethodPost {
				t.Error("wake must use POST")
			}
			if wakeStatus != http.StatusOK {
				http.Error(w, "synthetic wake failure", wakeStatus)
				return
			}
			running.Store(true)
			writeJSON(w, map[string]any{"status": "wake_requested"})
		case "/api/hatch/token":
			f.tokenCalls.Add(1)
			m.server.Config.Handler.ServeHTTP(w, r)
		default:
			m.server.Config.Handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(bootstrap.Close)
	f.client = New(Config{
		BaseURL: bootstrap.URL, WSURL: "ws" + strings.TrimPrefix(ws.URL, "http") + "/v1/noise",
		Cookies: map[string]string{"hatch_sess": "synthetic-session"},
	})
	t.Cleanup(func() { _ = f.client.Close() })
	return f
}

func TestUnknownColdVMWakesWithoutWaitingForRunningState(t *testing.T) {
	f := newUnknownWakeFixture(t, http.StatusOK, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probe, err := f.client.Probe(ctx)
	if err != nil {
		t.Fatalf("assigned UNKNOWN cold gateway must be woken before Noise probe: %v", err)
	}
	if probe.Status != "ok" || probe.VMState != "UNKNOWN" {
		t.Fatalf("probe status=%q vm_state=%q; preserve the control-plane observation", probe.Status, probe.VMState)
	}
	result, err := f.client.Generate(ctx, GenerateRequest{NodeID: "node-cold-unknown", Prompt: "hello"}, nil)
	if err != nil || result.Text != "hello world" {
		t.Fatalf("generation through awakened UNKNOWN gateway: text=%q err=%v", result.Text, err)
	}
	if f.sessionCalls.Load() != 1 || f.wakeCalls.Load() != 1 || f.tokenCalls.Load() != 1 || f.wsCalls.Load() != 1 {
		t.Fatalf("session=%d wake=%d token=%d ws=%d; want one wake without readiness polling", f.sessionCalls.Load(), f.wakeCalls.Load(), f.tokenCalls.Load(), f.wsCalls.Load())
	}
}

func TestUnknownWakeAuthenticationAndRateLimitStopBeforeGateway(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		status     int
	}{
		{"unauthenticated", "auth", http.StatusUnauthorized},
		{"rate_limited", "rate_limit", http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUnknownWakeFixture(t, tc.status, true)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := f.client.Connect(ctx)
			var he *Error
			if !errors.As(err, &he) || he.Kind != tc.kind {
				t.Errorf("wake failure err=%v; want kind %s", err, tc.kind)
			}
			if f.sessionCalls.Load() != 1 || f.wakeCalls.Load() != 1 || f.tokenCalls.Load() != 0 || f.wsCalls.Load() != 0 {
				t.Errorf("session=%d wake=%d token=%d ws=%d; wake authentication/rate failure must stop the connection", f.sessionCalls.Load(), f.wakeCalls.Load(), f.tokenCalls.Load(), f.wsCalls.Load())
			}
		})
	}
}

func TestUnknownWakeServerFailureDoesNotBlockRunningGateway(t *testing.T) {
	f := newUnknownWakeFixture(t, http.StatusServiceUnavailable, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if probe, err := f.client.Probe(ctx); err != nil || probe.Status != "ok" || probe.VMState != "UNKNOWN" {
		t.Fatalf("already running UNKNOWN gateway remains reachable after wake 503: status=%q state=%q err=%v", probe.Status, probe.VMState, err)
	}
	if f.sessionCalls.Load() != 1 || f.wakeCalls.Load() != 1 || f.tokenCalls.Load() != 1 || f.wsCalls.Load() != 1 {
		t.Fatalf("session=%d wake=%d token=%d ws=%d; want one best-effort wake and one gateway connection", f.sessionCalls.Load(), f.wakeCalls.Load(), f.tokenCalls.Load(), f.wsCalls.Load())
	}
}
