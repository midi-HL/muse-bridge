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

type gatewayRetryFixture struct {
	client       *Client
	sessionCalls atomic.Int32
	wakeCalls    atomic.Int32
	tokenCalls   atomic.Int32
	wsCalls      atomic.Int32
}

// A successful wake only accepts a request to resume the workspace. Its
// gateway can still respond with a temporary HTTP error during preparation.
// Use deterministic handshake statuses rather than sleeps for that transition.
func newGatewayRetryFixture(t *testing.T, statuses []int, repeatLast bool) *gatewayRetryFixture {
	t.Helper()
	f := &gatewayRetryFixture{}
	m := newMockHatch(t)
	t.Cleanup(m.close)
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(f.wsCalls.Add(1)) - 1
		status := http.StatusSwitchingProtocols
		if index < len(statuses) {
			status = statuses[index]
		} else if repeatLast && len(statuses) != 0 {
			status = statuses[len(statuses)-1]
		}
		if status != http.StatusSwitchingProtocols {
			http.Error(w, "synthetic gateway preparation", status)
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

func TestUnknownWokenGatewayRetriesTemporaryUpgradeFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []int
	}{
		{"bad_gateway", []int{http.StatusBadGateway}},
		{"unavailable", []int{http.StatusServiceUnavailable}},
		{"two_preparation_responses", []int{http.StatusBadGateway, http.StatusServiceUnavailable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGatewayRetryFixture(t, tc.statuses, false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			probe, err := f.client.Probe(ctx)
			if err != nil || probe.Status != "ok" || probe.VMState != "UNKNOWN" {
				t.Fatalf("woken UNKNOWN gateway should become reachable after temporary upgrade failures: probe=%+v err=%v", probe, err)
			}
			result, err := f.client.Generate(ctx, GenerateRequest{NodeID: "node-unknown-gateway-retry", Prompt: "hello"}, nil)
			if err != nil || result.Text != "hello world" {
				t.Fatalf("generate after gateway preparation: text=%q err=%v", result.Text, err)
			}
			wantAttempts := int32(len(tc.statuses) + 1)
			if f.sessionCalls.Load() != 1 || f.wakeCalls.Load() != 1 || f.tokenCalls.Load() != wantAttempts || f.wsCalls.Load() != wantAttempts {
				t.Fatalf("session=%d wake=%d token=%d ws=%d; want one wake and %d fresh-token upgrades", f.sessionCalls.Load(), f.wakeCalls.Load(), f.tokenCalls.Load(), f.wsCalls.Load(), wantAttempts)
			}
		})
	}
}

func TestUnknownWokenGatewayDoesNotRetryAuthenticationOrRateLimit(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newGatewayRetryFixture(t, []int{status}, false)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := f.client.Connect(ctx); err == nil {
				t.Fatal("gateway rejection must stop rather than retry into a successful fixture connection")
			}
			if f.sessionCalls.Load() != 1 || f.wakeCalls.Load() != 1 || f.tokenCalls.Load() != 1 || f.wsCalls.Load() != 1 {
				t.Fatalf("session=%d wake=%d token=%d ws=%d; gateway authentication/rate rejection must not retry", f.sessionCalls.Load(), f.wakeCalls.Load(), f.tokenCalls.Load(), f.wsCalls.Load())
			}
		})
	}
}

func TestUnknownWokenGatewayPreparationRespectsContextDeadline(t *testing.T) {
	f := newGatewayRetryFixture(t, []int{http.StatusBadGateway}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := f.client.Probe(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("permanent preparation failure must stop at caller deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("gateway preparation ignored short caller deadline: %s", elapsed)
	}
	if f.sessionCalls.Load() != 1 || f.wakeCalls.Load() != 1 || f.wsCalls.Load() == 0 {
		t.Fatalf("session=%d wake=%d token=%d ws=%d; preparation retry must reuse its single wake", f.sessionCalls.Load(), f.wakeCalls.Load(), f.tokenCalls.Load(), f.wsCalls.Load())
	}
}
