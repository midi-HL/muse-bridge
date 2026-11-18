package core

import (
	"context"
	"testing"
)

func TestProtocolProbeEmptyPoolIsExplicit(t *testing.T) {
	s, _ := testService(t)
	out, err := s.Action(context.Background(), "protocol.probe", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["status"] != "empty_pool" || m["remote_tasks"] != nil {
		t.Fatalf("unexpected empty probe: %#v", m)
	}
}
