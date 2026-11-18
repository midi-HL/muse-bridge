package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatPersonaFlagUsesServer(t *testing.T) {
	var selection string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		selection, _ = body["persona_id"].(string)
		io.WriteString(w, `{"text":"ok"}`)
	}))
	defer ts.Close()
	out := &bytes.Buffer{}
	r := Runner{Out: out, Env: func(k string) string {
		if k == "MUSE_API_KEY" {
			return "synthetic-key"
		}
		return ""
	}}
	if err := r.Run(context.Background(), []string{"--url", ts.URL, "chat", "--prompt", "hello", "--persona", "calm", "--stream=false"}); err != nil {
		t.Fatal(err)
	}
	if selection != "calm" || out.String() != "ok\n" {
		t.Fatal("CLI did not send persona selection")
	}
}

func TestEphemeralChatRejectsSavedPersonaWithoutNetwork(t *testing.T) {
	called := false
	r := Runner{Out: io.Discard, Env: func(string) string { return "" }, EphemeralChat: func(context.Context, string, string, string, func(string) error) error { called = true; return nil }}
	if err := r.Run(context.Background(), []string{"chat", "--prompt", "hello", "--persona", "calm", "--cookie", "env:SYNTHETIC"}); err == nil || called {
		t.Fatal("ephemeral chat silently ignored saved persona")
	}
}
