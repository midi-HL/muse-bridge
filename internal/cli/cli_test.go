package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIUsesRegistryAndDoesNotPrintSecret(t *testing.T) {
	secret := "synthetic-only-key-value"
	var called string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/api/auth/status" {
			io.WriteString(w, `{"authenticated":true,"csrf_token":"synthetic-csrf"}`)
			return
		}
		if r.Header.Get("Origin") == "" || r.Header.Get("X-CSRF-Token") != "synthetic-csrf" {
			t.Error("missing origin or CSRF")
		}
		c, e := r.Cookie("muse_admin")
		if e != nil || c.Value != "synthetic-session" {
			t.Error("missing cookie")
		}
		var v struct {
			Action string `json:"action"`
		}
		json.NewDecoder(r.Body).Decode(&v)
		called = v.Action
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"id": "id1", "key": secret, "name": "test"}})
	}))
	defer ts.Close()
	out := &bytes.Buffer{}
	r := Runner{Out: out, Env: func(k string) string {
		if k == "MUSE_ADMIN_COOKIE" {
			return "synthetic-session"
		}
		return ""
	}}
	file := filepath.Join(t.TempDir(), "key")
	e := r.Run(context.Background(), []string{"--url", ts.URL, "keys", "create", "--name", "test", "--out", file})
	if e != nil {
		t.Fatal(e)
	}
	if called != "keys.create" {
		t.Fatal("not shared registry")
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("key printed")
	}
	b, e := os.ReadFile(file)
	if e != nil || strings.TrimSpace(string(b)) != secret {
		t.Fatal("key not saved")
	}
	before := called
	called = ""
	e = r.Run(context.Background(), []string{"--url", ts.URL, "keys", "create", "--name", "test", "--out", file})
	if e == nil || called != "" || before == "" {
		t.Fatal("overwrote private file or created abandoned key")
	}
}
func TestCookieInputOnlyEnvironmentOrFile(t *testing.T) {
	out := &bytes.Buffer{}
	called := false
	r := Runner{Out: out, Env: func(k string) string {
		if k == "TEST_COOKIE" {
			return "synthetic-cookie"
		}
		return ""
	}, EphemeralChat: func(ctx context.Context, cookie, prompt, model string, emit func(string) error) error {
		called = true
		if cookie != "synthetic-cookie" {
			t.Error("wrong source")
		}
		return emit("hello")
	}}
	e := r.Run(context.Background(), []string{"chat", "--cookie", "sensitive-raw-value", "--prompt", "hi"})
	if e == nil || called || strings.Contains(e.Error(), "sensitive-raw-value") {
		t.Fatal("raw cookie accepted or echoed")
	}
	if e = r.Run(context.Background(), []string{"chat", "--cookie", "env:TEST_COOKIE", "--prompt", "hi"}); e != nil {
		t.Fatal(e)
	}
	if !called || out.String() != "hello\n" {
		t.Fatal("ephemeral call failed")
	}
}
func TestCLIRefusesRemoteCleartextAndSecretArguments(t *testing.T) {
	r := Runner{Out: io.Discard, Env: func(string) string { return "" }}
	if e := r.Run(context.Background(), []string{"--url", "http://example.com", "status"}); e == nil {
		t.Fatal("cleartext remote accepted")
	}
	if e := r.Run(context.Background(), []string{"config", "set", "telegram.token", "--value", `"synthetic"`}); e == nil {
		t.Fatal("secret argument accepted")
	}
}
func TestCLIChatStream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("missing external API key")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"delta\",\"text\":\"hello\"}\n\ndata: {\"type\":\"done\",\"text\":\"hello\"}\n\n")
	}))
	defer ts.Close()
	out := &bytes.Buffer{}
	r := Runner{Out: out, Env: func(k string) string {
		if k == "MUSE_API_KEY" {
			return "synthetic-key"
		}
		return ""
	}}
	if e := r.Run(context.Background(), []string{"--url", ts.URL, "chat", "--prompt", "hi"}); e != nil {
		t.Fatal(e)
	}
	if out.String() != "hello\n" {
		t.Fatal("duplicated SSE final text")
	}
}
func TestGenericActionUsesSharedRegistry(t *testing.T) {
	var called string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/api/auth/status" {
			io.WriteString(w, `{"authenticated":true,"csrf_token":"csrf"}`)
			return
		}
		var body struct {
			Action string         `json:"action"`
			Args   map[string]any `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		called = body.Action
		if body.Args["name"] != "demo" {
			t.Errorf("args were not sent: %#v", body.Args)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"ok": true}})
	}))
	defer ts.Close()
	out := &bytes.Buffer{}
	r := Runner{Out: out, Env: func(k string) string {
		if k == "MUSE_ADMIN_COOKIE" {
			return "session"
		}
		return ""
	}}
	if err := r.Run(context.Background(), []string{"--url", ts.URL, "action", "personas.save", "--args", `{"name":"demo","prompt":"hi"}`}); err != nil {
		t.Fatal(err)
	}
	if called != "personas.save" {
		t.Fatalf("wrong action: %s", called)
	}
}

func TestCredentialResponseErrorsNeverEchoBodies(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, "synthetic-secret-error-body")
	}))
	defer ts.Close()
	r := Runner{Out: io.Discard, Env: func(string) string { return "" }}
	e := r.Run(context.Background(), []string{"--url", ts.URL, "status"})
	if e == nil || strings.Contains(e.Error(), "synthetic-secret") {
		t.Fatal("error body exposed")
	}
}
