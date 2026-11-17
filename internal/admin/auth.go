package admin

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Auth struct {
	db         *store.Store
	mu         sync.Mutex
	attempts   map[string]*attempt
	setupToken string
}
type attempt struct {
	Count int
	Reset time.Time
}
type authRecord struct {
	Hash        string `json:"hash"`
	TOTP        string `json:"totp,omitempty"`
	PendingTOTP string `json:"pending_totp,omitempty"`
	Generation  string `json:"generation"`
}
type sessionRecord struct {
	CSRF       string    `json:"csrf"`
	Expires    time.Time `json:"expires"`
	Generation string    `json:"generation"`
}

func NewAuth(db *store.Store, token string) *Auth {
	return &Auth{db: db, attempts: map[string]*attempt{}, setupToken: token}
}
func (a *Auth) record(ctx context.Context) (authRecord, error) {
	var r authRecord
	e := a.db.Secret(ctx, "auth", "admin", &r)
	return r, e
}
func (a *Auth) SetupRequired(ctx context.Context) bool {
	_, e := a.record(ctx)
	return errors.Is(e, sql.ErrNoRows)
}
func (a *Auth) save(ctx context.Context, r authRecord) error {
	return a.db.Put(ctx, "auth", "admin", map[string]any{"configured": true, "totp_enabled": r.TOTP != ""}, r)
}
func (a *Auth) Allow(r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range a.attempts {
		if now.After(v.Reset) {
			delete(a.attempts, k)
		}
	}
	v := a.attempts[host]
	if v == nil {
		if len(a.attempts) >= 4096 {
			return false
		}
		v = &attempt{Reset: now.Add(10 * time.Minute)}
		a.attempts[host] = v
	}
	v.Count++
	return v.Count <= 10
}
func (a *Auth) Login(ctx context.Context, password, otp string) (string, string, error) {
	r, e := a.record(ctx)
	if e != nil || !secure.VerifyPassword(password, r.Hash) {
		return "", "", errors.New("invalid password or verification code")
	}
	if r.TOTP != "" && !secure.VerifyTOTP(r.TOTP, otp, time.Now()) {
		return "", "", errors.New("invalid password or verification code")
	}
	return a.newSession(ctx, r.Generation)
}
func (a *Auth) Setup(ctx context.Context, password, token string) (string, string, error) {
	if !a.SetupRequired(ctx) {
		return "", "", errors.New("administrator already configured")
	}
	if a.setupToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(a.setupToken)) != 1 {
		return "", "", errors.New("setup token required")
	}
	if !secure.ValidPassword(password) {
		return "", "", errors.New("password must contain 12–256 bytes")
	}
	r := authRecord{Hash: secure.PasswordHash(password), Generation: secure.ID()}
	if e := a.save(ctx, r); e != nil {
		return "", "", e
	}
	return a.newSession(ctx, r.Generation)
}
func (a *Auth) newSession(ctx context.Context, generation string) (string, string, error) {
	token, csrf := secure.Random(32), secure.Random(24)
	s := sessionRecord{CSRF: csrf, Expires: time.Now().Add(12 * time.Hour), Generation: generation}
	e := a.db.Put(ctx, "admin_session", secure.Digest(token), map[string]any{"expires": s.Expires}, s)
	return token, csrf, e
}
func (a *Auth) Session(r *http.Request) (sessionRecord, bool) {
	token := ""
	if c, e := r.Cookie("muse_admin"); e == nil {
		token = c.Value
	}
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if token == "" {
		return sessionRecord{}, false
	}
	var s sessionRecord
	if a.db.Secret(r.Context(), "admin_session", secure.Digest(token), &s) != nil || time.Now().After(s.Expires) {
		return sessionRecord{}, false
	}
	rec, e := a.record(r.Context())
	if e != nil || s.Generation != rec.Generation {
		return sessionRecord{}, false
	}
	return s, true
}
func (a *Auth) Logout(r *http.Request) {
	token := ""
	if c, e := r.Cookie("muse_admin"); e == nil {
		token = c.Value
	}
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if token != "" {
		_ = a.db.Delete(r.Context(), "admin_session", secure.Digest(token))
	}
}
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	age := 12 * 3600
	if token == "" {
		age = -1
	}
	http.SetCookie(w, &http.Cookie{Name: "muse_admin", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host {
			return false
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if u.Scheme != scheme {
			return false
		}
	}
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
func csrfOK(r *http.Request, s sessionRecord) bool {
	return sameOrigin(r) && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF)) == 1
}
func (a *Auth) Action(ctx context.Context, name string, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, e := a.record(ctx)
	if e != nil {
		return nil, e
	}
	str := func(k string) string { s, _ := args[k].(string); return s }
	switch name {
	case "auth.totp.setup":
		if r.TOTP != "" {
			return nil, errors.New("two-step verification is already enabled")
		}
		r.PendingTOTP = secure.NewTOTPSecret()
		if e = a.save(ctx, r); e != nil {
			return nil, e
		}
		return map[string]any{"secret": r.PendingTOTP, "otpauth_url": "otpauth://totp/Muse%20Bridge:admin?secret=" + r.PendingTOTP + "&issuer=Muse%20Bridge&algorithm=SHA1&digits=6&period=30"}, nil
	case "auth.totp.enable":
		if r.PendingTOTP == "" || !secure.VerifyTOTP(r.PendingTOTP, str("otp"), time.Now()) {
			return nil, errors.New("invalid verification code")
		}
		r.TOTP = r.PendingTOTP
		r.PendingTOTP = ""
		r.Generation = secure.ID()
		return map[string]any{"reauthenticate": true}, a.save(ctx, r)
	case "auth.totp.disable":
		if !secure.VerifyPassword(str("password"), r.Hash) || r.TOTP == "" || !secure.VerifyTOTP(r.TOTP, str("otp"), time.Now()) {
			return nil, errors.New("invalid password or verification code")
		}
		r.TOTP = ""
		r.PendingTOTP = ""
		r.Generation = secure.ID()
		return map[string]any{"reauthenticate": true}, a.save(ctx, r)
	case "auth.password":
		if !secure.VerifyPassword(str("current_password"), r.Hash) || (r.TOTP != "" && !secure.VerifyTOTP(r.TOTP, str("otp"), time.Now())) {
			return nil, errors.New("invalid password or verification code")
		}
		if !secure.ValidPassword(str("password")) {
			return nil, errors.New("password must contain 12–256 bytes")
		}
		r.Hash = secure.PasswordHash(str("password"))
		r.Generation = secure.ID()
		return map[string]any{"reauthenticate": true}, a.save(ctx, r)
	}
	return nil, errors.New("unknown authentication operation")
}
