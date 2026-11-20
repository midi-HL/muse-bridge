package hatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A live session lookup can report UNKNOWN while its assigned Noise gateway
// already serves requests. Exercise the complete bootstrap, encrypted ping and
// generation path rather than treating the state label itself as a health check.
func TestAssignedUnknownVMProbeAndGenerate(t *testing.T) {
	m := newMockHatch(t)
	defer m.close()
	bootstrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session" {
			writeJSON(w, map[string]any{
				"status": "assigned", "vm_state": "UNKNOWN",
				"vm_id": "vm-test", "endpoint_url": "https://vm.test", "vm_name": "vm",
			})
			return
		}
		m.server.Config.Handler.ServeHTTP(w, r)
	}))
	defer bootstrap.Close()
	c := New(Config{BaseURL: bootstrap.URL, WSURL: m.wsURL, Cookies: map[string]string{
		"hatch_sess": "synthetic-session",
	}})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	probe, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("assigned UNKNOWN gateway should be probed through Noise: %v", err)
	}
	if probe.Status != "ok" || probe.VMState != "UNKNOWN" {
		t.Fatalf("probe status=%q vm_state=%q; preserve the reported state after a successful ping", probe.Status, probe.VMState)
	}
	result, err := c.Generate(ctx, GenerateRequest{NodeID: "node-unknown-state", Prompt: "hello"}, nil)
	if err != nil {
		t.Fatalf("generate through assigned UNKNOWN gateway: %v", err)
	}
	if result.Text != "hello world" {
		t.Fatalf("generated text=%q", result.Text)
	}
}

func TestBootstrapReadinessStateAndAssignment(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		want        bool
	}{
		{"running", "RUNNING", true},
		{"unspecified", "", true},
		{"unknown", "UNKNOWN", true},
		{"disabled", "DISABLED", false},
		{"stopped", "STOPPED", false},
		{"starting", "STARTING", false},
		{"stopping", "STOPPING", false},
		{"error", "ERROR", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := map[string]any{
				"status": "assigned", "vm_state": tc.state,
				"vm_id": "vm-test", "endpoint_url": "https://vm.test",
			}
			if got := ready(session); got != tc.want {
				t.Fatalf("ready(state=%q)=%t, want %t", tc.state, got, tc.want)
			}
		})
	}
	for _, missing := range []string{"status", "vm_id", "endpoint_url"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			session := map[string]any{
				"status": "assigned", "vm_state": "UNKNOWN",
				"vm_id": "vm-test", "endpoint_url": "https://vm.test",
			}
			delete(session, missing)
			if ready(session) {
				t.Fatalf("UNKNOWN session without %s must remain pending", missing)
			}
		})
	}
}
