package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCLIChatStreamLimitErrors(t *testing.T) {
	const secret = "synthetic-secret-74f8"
	for _, tt := range []struct {
		name, code, want string
	}{
		{"daily limit", "daily_limit", "daily chat request limit reached"},
		{"queue full", "queue_full", "request queue is full; retry later"},
		{"unknown error", "daily_limit_" + secret, "generation failed; check service account availability"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/chat/stream" {
					t.Errorf("expected default SSE endpoint, got %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"error\",\"error\":{\"code\":%q,\"message\":%q},\"text\":%q}\n\n", tt.code, "message: "+secret, "text: "+secret)
			}))
			defer ts.Close()
			var out, stderr bytes.Buffer
			r := Runner{Out: &out, Err: &stderr, Env: func(name string) string {
				if name == "MUSE_API_KEY" {
					return "synthetic-key"
				}
				return ""
			}}
			err := r.Run(context.Background(), []string{"--url", ts.URL, "chat", "--prompt", "hi"})
			if err == nil {
				t.Fatal("SSE error must return a command error")
			}
			if strings.Contains(err.Error()+out.String()+stderr.String(), secret) {
				t.Fatal("CLI leaked server error details")
			}
			if err.Error() != tt.want {
				t.Errorf("got error %q, want %q", err, tt.want)
			}
			if out.Len() != 0 || stderr.Len() != 0 {
				t.Error("SSE error unexpectedly wrote to command output")
			}
		})
	}
}

func TestCLIChatNonStreamLimitErrors(t *testing.T) {
	const secret = "synthetic-secret-74f8"
	const generic = "service returned HTTP 429; check authorization, input or feature support"
	for _, tt := range []struct {
		name, body, want string
	}{
		{"daily limit", fmt.Sprintf(`{"error":{"code":"daily_limit","message":%q}}`, secret), "daily chat request limit reached"},
		{"queue full", fmt.Sprintf(`{"error":{"code":"queue_full","message":%q}}`, secret), "request queue is full; retry later"},
		{"unknown error", fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, "daily_limit_"+secret, secret), generic},
		{"malformed error", fmt.Sprintf(`{"error":{"code":"daily_limit","message":%q}`, secret), generic},
		{"oversized error", fmt.Sprintf(`{"error":{"code":"daily_limit","message":%q}}`, strings.Repeat(secret, 4096)), generic},
		{"trailing JSON", fmt.Sprintf(`{"error":{"code":"daily_limit"}}{"secret":%q}`, secret), generic},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
					t.Errorf("expected non-stream chat endpoint, got %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Error("missing external API key")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, tt.body)
			}))
			defer ts.Close()
			var out, stderr bytes.Buffer
			r := Runner{Out: &out, Err: &stderr, Env: func(name string) string {
				if name == "MUSE_API_KEY" {
					return "synthetic-key"
				}
				return ""
			}}
			err := r.Run(context.Background(), []string{"--url", ts.URL, "chat", "--stream=false", "--prompt", "hi"})
			if err == nil {
				t.Fatal("HTTP 429 must return a command error")
			}
			if strings.Contains(err.Error()+out.String()+stderr.String(), secret) {
				t.Fatal("CLI leaked server error details")
			}
			if err.Error() != tt.want {
				t.Errorf("got error %q, want %q", err, tt.want)
			}
			if out.Len() != 0 || stderr.Len() != 0 {
				t.Error("HTTP 429 unexpectedly wrote to command output")
			}
		})
	}
}

func TestClientChatLimitErrorsStayScoped(t *testing.T) {
	for _, tt := range []struct {
		name, path string
		api        bool
		status     int
		want       string
	}{
		{"stream API 429", "/api/chat/stream", true, http.StatusTooManyRequests, "daily chat request limit reached"},
		{"chat unauthorized", "/api/chat", true, http.StatusUnauthorized, ""},
		{"chat forbidden", "/api/chat", true, http.StatusForbidden, ""},
		{"chat bad request", "/api/chat", true, http.StatusBadRequest, ""},
		{"chat service failure", "/api/chat", true, http.StatusInternalServerError, ""},
		{"admin 429", "/admin/api/auth/status", false, http.StatusTooManyRequests, ""},
		{"other API 429", "/api/other", true, http.StatusTooManyRequests, ""},
		{"chat prefix 429", "/api/chat/other", true, http.StatusTooManyRequests, ""},
		{"non-API chat 429", "/api/chat", false, http.StatusTooManyRequests, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const secret = "synthetic-secret-74f8"
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					t.Errorf("got request path %s, want %s", r.URL.Path, tt.path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				fmt.Fprintf(w, `{"error":{"code":"daily_limit","message":%q}}`, secret)
			}))
			defer ts.Close()
			c := client{base: ts.URL, cred: credentials{APIKey: "synthetic-key"}, http: ts.Client()}
			_, err := c.request(context.Background(), http.MethodPost, tt.path, nil, tt.api)
			if err == nil {
				t.Fatal("HTTP failure must return a request error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("CLI leaked server error details")
			}
			want := tt.want
			if want == "" {
				want = fmt.Sprintf("service returned HTTP %d; check authorization, input or feature support", tt.status)
			}
			if err.Error() != want {
				t.Errorf("got error %q, want %q", err, want)
			}
		})
	}
}
