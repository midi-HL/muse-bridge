package httpapi

import (
	"strings"
	"testing"
)

func TestAllChatEndpointsForwardPersonaSelection(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		path, body string
		admin      bool
	}{{"/admin/api/chat", `{"prompt":"hi","persona_id":"calm"}`, true}, {"/api/chat", `{"prompt":"hi","persona_id":"calm"}`, false}, {"/api/chat/stream", `{"prompt":"hi","persona_id":"calm"}`, false}, {"/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}],"persona_id":"calm"}`, false}, {"/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}],"stream":true,"persona_id":"_none"}`, false}} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			key, origin := h.key, ""
			if tc.admin {
				key = ""
				origin = "http://127.0.0.1:8787"
			}
			w := h.call("POST", tc.path, tc.body, origin, tc.admin, key)
			if w.Code != 200 {
				t.Fatalf("status=%d", w.Code)
			}
			got := h.backend.requests[len(h.backend.requests)-1]
			want := "calm"
			if strings.Contains(tc.body, `"_none"`) {
				want = "_none"
			}
			if got.PersonaID != want {
				t.Fatalf("persona_id=%q want=%q", got.PersonaID, want)
			}
		})
	}
}
