// Package httpapi is the shared authenticated HTTP boundary for the UI and CLI.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/midi-HL/muse-bridge/internal/admin"
	"github.com/midi-HL/muse-bridge/internal/config"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/store"
	"github.com/midi-HL/muse-bridge/web"
)

// Backend permits the transport to be tested independently of live Muse accounts.
type Backend interface {
	State(context.Context) (map[string]any, error)
	Action(context.Context, string, map[string]any) (any, error)
	Chat(context.Context, core.ChatRequest, func(core.ChatEvent) error) (core.ChatResult, error)
	File(context.Context, string) (string, core.File, error)
}
type Options struct {
	Auth               *admin.Auth
	Keys               *admin.Keys
	Backend            Backend
	LoopbackOnly       bool
	SetupTokenRequired bool
	TelegramWebhook    http.Handler
	TelegramSecret     func() string
}
type telegramWebhookGuard struct {
	next   http.Handler
	secret func() string
}

func (g telegramWebhookGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	want := ""
	if g.secret != nil {
		want = g.secret()
	}
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))) != 1 {
		failure(w, http.StatusForbidden, "forbidden", "Telegram webhook verification failed")
		return
	}
	g.next.ServeHTTP(w, r)
}

type Server struct {
	auth               *admin.Auth
	keys               *admin.Keys
	backend            Backend
	loopbackOnly       bool
	setupTokenRequired bool
	mux                *http.ServeMux
}

func New(o Options) *Server {
	s := &Server{auth: o.Auth, keys: o.Keys, backend: o.Backend, loopbackOnly: o.LoopbackOnly, setupTokenRequired: o.SetupTokenRequired, mux: http.NewServeMux()}
	if o.TelegramWebhook != nil {
		s.mux.Handle("/telegram/webhook", telegramWebhookGuard{o.TelegramWebhook, o.TelegramSecret})
	}
	s.mux.HandleFunc("/admin/api/auth/status", s.authStatus)
	s.mux.HandleFunc("/admin/api/auth/setup", s.authSetup)
	s.mux.HandleFunc("/admin/api/auth/login", s.authLogin)
	s.mux.HandleFunc("/admin/api/auth/logout", s.authLogout)
	for _, prefix := range []string{"", "/admin/api"} {
		s.mux.HandleFunc(prefix+"/schema", s.schema)
		s.mux.HandleFunc(prefix+"/state", s.state)
		s.mux.HandleFunc(prefix+"/action", s.action)
		s.mux.HandleFunc(prefix+"/chat", s.chat)
	}
	s.mux.HandleFunc("/admin/api/chat/stop", s.stop)
	s.mux.HandleFunc("/v1/models", s.models)
	s.mux.HandleFunc("/v1/chat/completions", s.completions)
	s.mux.HandleFunc("/api/chat", s.simpleChat)
	s.mux.HandleFunc("/api/chat/stream", s.simpleChat)
	s.mux.HandleFunc("/files/", s.file)
	s.mux.HandleFunc("/admin/api/files/", s.file)
	s.mux.HandleFunc("/admin/api/", func(w http.ResponseWriter, r *http.Request) { failure(w, 404, "not_found", "Endpoint not found") })
	s.mux.Handle("/", web.Handler())
	return s
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Cache-Control", "no-store")
	if s.loopbackOnly && !loopbackHost(r.Host) {
		failure(w, 403, "forbidden", "Host is not allowed")
		return
	}
	s.mux.ServeHTTP(w, r)
}
func loopbackHost(host string) bool {
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}
func method(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method != want {
		w.Header().Set("Allow", want)
		failure(w, 405, "method_not_allowed", "Method not allowed")
		return false
	}
	return true
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, code, message string) {
	jsonResponse(w, status, map[string]any{"error": map[string]any{"code": code, "type": code, "message": message, "fields": map[string]any{}}})
}
func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		failure(w, 415, "invalid_request", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	if e = d.Decode(v); e != nil {
		failure(w, 400, "invalid_request", "Invalid or oversized JSON body")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		failure(w, 400, "invalid_request", "Exactly one JSON value is required")
		return false
	}
	return true
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, e := url.Parse(origin)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
func cookieRequest(r *http.Request) *http.Request {
	c := r.Clone(r.Context())
	c.Header = r.Header.Clone()
	c.Header.Del("Authorization")
	return c
}
func (s *Server) authenticated(w http.ResponseWriter, r *http.Request, write bool) bool {
	if s.auth == nil {
		failure(w, 503, "unavailable", "Authentication is not ready")
		return false
	}
	session, ok := s.auth.Session(cookieRequest(r))
	if !ok {
		failure(w, 401, "unauthorized", "Administrator login required")
		return false
	}
	if write && (!sameOrigin(r) || subtle.ConstantTimeCompare([]byte(session.CSRF), []byte(r.Header.Get("X-CSRF-Token"))) != 1) {
		failure(w, 403, "csrf_failed", "Origin or CSRF verification failed")
		return false
	}
	return true
}
func sessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	age := 43200
	if token == "" {
		age = -1
	}
	http.SetCookie(w, &http.Cookie{Name: "muse_admin", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		failure(w, 503, "unavailable", "Authentication is not ready")
		return
	}
	if !method(w, r, http.MethodGet) {
		return
	}
	session, ok := s.auth.Session(cookieRequest(r))
	out := map[string]any{"setup_required": s.auth.SetupRequired(r.Context()), "setup_token_required": s.setupTokenRequired, "authenticated": ok}
	if ok {
		out["csrf_token"] = session.CSRF
	}
	jsonResponse(w, 200, out)
}
func (s *Server) credentials(w http.ResponseWriter, r *http.Request, setup bool) {
	if !method(w, r, http.MethodPost) {
		return
	}
	if !sameOrigin(r) {
		failure(w, 403, "csrf_failed", "Same-origin request required")
		return
	}
	if !s.auth.Allow(r) {
		w.Header().Set("Retry-After", "600")
		failure(w, 429, "rate_limit", "Too many authentication attempts")
		return
	}
	var b struct {
		Password string `json:"password"`
		Repeat   string `json:"password_repeat"`
		OTP      string `json:"otp"`
		Token    string `json:"setup_token"`
	}
	if !decode(w, r, &b, 4096) {
		return
	}
	if len(b.Password) > 256 || len(b.OTP) > 32 || len(b.Token) > 4096 {
		failure(w, 400, "invalid_request", "Invalid credential length")
		return
	}
	if setup && (len(b.Password) < 12 || b.Repeat != b.Password) {
		failure(w, 400, "invalid_request", "Use a matching password of 12–256 bytes")
		return
	}
	var token, csrf string
	var err error
	if setup {
		token, csrf, err = s.auth.Setup(r.Context(), b.Password, b.Token)
	} else {
		token, csrf, err = s.auth.Login(r.Context(), b.Password, b.OTP)
	}
	if err != nil {
		failure(w, 401, "authentication_failed", "Authentication failed; check credentials and setup state")
		return
	}
	sessionCookie(w, r, token)
	jsonResponse(w, 200, map[string]any{"authenticated": true, "csrf_token": csrf})
}
func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) { s.credentials(w, r, true) }
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) { s.credentials(w, r, false) }
func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !s.authenticated(w, r, true) {
		return
	}
	s.auth.Logout(cookieRequest(r))
	sessionCookie(w, r, "")
	jsonResponse(w, 200, map[string]any{"ok": true})
}
func (s *Server) schema(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) || !s.authenticated(w, r, false) {
		return
	}
	jsonResponse(w, 200, map[string]any{"settings": config.Fields, "operations": Operations()})
}
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) || !s.authenticated(w, r, false) {
		return
	}
	if s.backend == nil || s.keys == nil {
		failure(w, 503, "unavailable", "Service is not ready")
		return
	}
	v, e := s.backend.State(r.Context())
	if e != nil {
		safeError(w, e)
		return
	}
	if v == nil {
		v = map[string]any{}
	}
	keys, e := s.keys.List(r.Context())
	if e != nil {
		safeError(w, e)
		return
	}
	v["api_keys"] = keys
	jsonResponse(w, 200, maskSettings(v))
}
func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !s.authenticated(w, r, true) {
		return
	}
	var b struct {
		Action string         `json:"action"`
		Args   map[string]any `json:"args"`
	}
	if !decode(w, r, &b, 64<<20) {
		return
	}
	result, e := s.Action(r.Context(), b.Action, b.Args)
	if e != nil {
		safeError(w, e)
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "data": result})
}

var ErrUnsupported = errors.New("unsupported operation")

func (s *Server) Action(ctx context.Context, name string, args map[string]any) (any, error) {
	if !knownAction(name) {
		return nil, ErrUnsupported
	}
	if s.auth == nil || s.keys == nil || s.backend == nil {
		return nil, errors.New("service unavailable")
	}
	if args == nil {
		args = map[string]any{}
	}
	if name == "chat.stop" {
		if stopper, ok := s.backend.(interface{ Stop(string) bool }); ok {
			id, _ := args["session_id"].(string)
			return map[string]any{"stopped": stopper.Stop(id)}, nil
		}
		return nil, ErrUnsupported
	}
	if name == "settings.update" {
		if values, ok := args["values"].(map[string]any); ok {
			args = values
		}
	}
	var out any
	var err error
	switch {
	case name == "keys.list":
		out, err = s.keys.List(ctx)
	case strings.HasPrefix(name, "keys."):
		out, err = s.keys.Action(ctx, name, args)
	case strings.HasPrefix(name, "auth."):
		out, err = s.auth.Action(ctx, name, args)
	default:
		out, err = s.backend.Action(ctx, name, args)
	}
	if name == "settings.update" {
		out = maskSettings(out)
	}
	return out, err
}
func safeError(w http.ResponseWriter, e error) {
	status, code, msg := errorInfo(e)
	failure(w, status, code, msg)
}
func errorInfo(e error) (int, string, string) {
	switch {
	case errors.Is(e, ErrUnsupported):
		return 501, "unsupported", "This operation is not supported"
	case errors.Is(e, store.ErrLimit):
		return 429, "rate_limit", "Request limit reached"
	case errors.Is(e, context.DeadlineExceeded):
		return 504, "timeout", "Request timed out"
	case errors.Is(e, context.Canceled):
		return 408, "cancelled", "Request cancelled"
	}
	var he *hatch.Error
	if errors.As(e, &he) {
		switch he.Kind {
		case "unsupported":
			return 501, "unsupported", "Upstream does not expose this operation"
		case "auth":
			return 502, "upstream_auth", "Upstream account authentication failed"
		case "rate_limit":
			return 429, "upstream_rate_limit", "Upstream rate limit reached"
		}
	}
	// Backend errors may carry credentials. Never forward their text.
	lower := strings.ToLower(e.Error())
	if strings.Contains(lower, "unsupported") || strings.Contains(lower, "unknown action") || strings.Contains(lower, "unknown operation") {
		return 501, "unsupported", "This operation is not supported"
	}
	return 400, "operation_failed", "Operation failed; check input, account availability and configuration"
}
func (s *Server) apiKey(w http.ResponseWriter, r *http.Request, charge bool) (admin.APIKey, bool) {
	if s.keys == nil {
		failure(w, 503, "unavailable", "API authentication is not ready")
		return admin.APIKey{}, false
	}
	header := r.Header.Get("Authorization")
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		w.Header().Set("WWW-Authenticate", "Bearer")
		failure(w, 401, "invalid_api_key", "Bearer API key required")
		return admin.APIKey{}, false
	}
	key, e := s.keys.Authenticate(r.Context(), parts[1], charge)
	if e != nil {
		if errors.Is(e, store.ErrLimit) {
			w.Header().Set("Retry-After", "60")
			failure(w, 429, "rate_limit", "API key request limit reached")
		} else {
			failure(w, 401, "invalid_api_key", "Invalid API key")
		}
		return admin.APIKey{}, false
	}
	return key, true
}
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		failure(w, 405, "method_not_allowed", "Method not allowed")
		return
	}
	if _, ok := s.auth.Session(cookieRequest(r)); !ok {
		if _, ok = s.apiKey(w, r, false); !ok {
			return
		}
	}
	id := strings.TrimPrefix(r.URL.Path, "/files/")
	if id == r.URL.Path {
		id = strings.TrimPrefix(r.URL.Path, "/admin/api/files/")
	}
	if id == "" || strings.ContainsAny(id, "/\\.") {
		failure(w, 404, "not_found", "File not found")
		return
	}
	path, meta, e := s.backend.File(r.Context(), id)
	if e != nil {
		failure(w, 404, "not_found", "File not found")
		return
	}
	f, e := os.Open(path)
	if e != nil {
		failure(w, 404, "not_found", "File not found")
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		failure(w, 404, "not_found", "File not found")
		return
	}
	name := filepath.Base(strings.ReplaceAll(meta.Filename, "\\", "/"))
	if name == "." || name == "" {
		name = "download"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// HTTPServer sets bounded header parsing while allowing long-lived SSE responses.
func HTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
}
