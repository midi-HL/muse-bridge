package httpapi

import (
	"net/http"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func TestProbeErrorsKeepWorkspaceAndCookieFailuresDistinct(t *testing.T) {
	cases := []struct {
		kind, code string
		status     int
	}{
		{kind: "pending_vm", code: "pending_vm", status: http.StatusServiceUnavailable},
		{kind: "auth", code: "upstream_auth", status: http.StatusBadGateway},
		{kind: "network", code: "upstream_unavailable", status: http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			status, code, _ := errorInfo(&hatch.Error{Kind: tc.kind, Op: "probe", Message: "synthetic failure"})
			if status != tc.status || code != tc.code {
				t.Fatalf("errorInfo = (%d,%q); want (%d,%q)", status, code, tc.status, tc.code)
			}
		})
	}
}
