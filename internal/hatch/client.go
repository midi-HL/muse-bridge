package hatch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"

// Client may be shared across goroutines. Generate calls are serialized because
// upstream presentation events can lack task attribution. Close is terminal.
type Client struct {
	mu                        sync.Mutex
	cfg                       Config
	http                      *http.Client
	initErr                   error
	cookies                   map[string]string
	vmState, status           string
	conn                      *connection
	closed                    bool
	connectGate, generateGate chan struct{}
	life                      context.Context
	cancel                    context.CancelFunc
}

func New(cfg Config) *Client {
	life, cancel := context.WithCancel(context.Background())
	c := &Client{cfg: cfg, cookies: make(map[string]string), connectGate: make(chan struct{}, 1), generateGate: make(chan struct{}, 1), life: life, cancel: cancel}
	c.connectGate <- struct{}{}
	c.generateGate <- struct{}{}
	for _, name := range []string{"hatch_gw", "hatch_sess", "hatch_vml"} {
		if v := cfg.Cookies[name]; v != "" {
			c.cookies[name] = v
		}
	}
	c.cfg.Cookies = nil
	if c.cfg.BaseURL == "" {
		c.cfg.BaseURL = "https://muse.ai"
	}
	c.cfg.BaseURL = strings.TrimRight(c.cfg.BaseURL, "/")
	if c.cfg.WSURL == "" {
		c.cfg.WSURL = "wss://hatch.metaaivm.com/v1/noise"
	}
	for i, s := range []string{c.cfg.BaseURL, c.cfg.WSURL} {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (i == 0 && u.Scheme != "https" && u.Scheme != "http") || (i == 1 && u.Scheme != "wss" && u.Scheme != "ws") {
			c.initErr = fault("protocol", "config", "invalid endpoint configuration")
		}
	}
	h := &http.Client{}
	if cfg.HTTPClient != nil {
		*h = *cfg.HTTPClient
	}
	h.Jar = nil // this client owns its account-specific cookie snapshot
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if cfg.ProxyURL != "" {
		proxy, err := url.Parse(cfg.ProxyURL)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5" && proxy.Scheme != "socks5h") {
			c.initErr = fault("network", "proxy", "invalid proxy configuration")
		} else {
			var tr *http.Transport
			if h.Transport == nil {
				tr = http.DefaultTransport.(*http.Transport).Clone()
			} else if old, ok := h.Transport.(*http.Transport); ok {
				tr = old.Clone()
			} else {
				c.initErr = fault("unsupported", "proxy", "custom transport cannot be combined with ProxyURL")
			}
			if tr != nil {
				tr.Proxy = http.ProxyURL(proxy)
				h.Transport = tr
			}
		}
	}
	c.http = h
	return c
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("hatch: random source unavailable")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return hex.EncodeToString(b[:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:])
}
func (c *Client) operation(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, d)
	stop := context.AfterFunc(c.life, cancel)
	return ctx, func() { stop(); cancel() }
}
func acquire(ctx context.Context, g chan struct{}) error {
	select {
	case <-ctx.Done():
		return contextError("wait", ctx.Err())
	case <-g:
		return nil
	}
}

// Cookies returns a copy for encrypted account persistence. Never log this map.
func (c *Client) Cookies() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.cookies))
	for k, v := range c.cookies {
		out[k] = v
	}
	return out
}
func (c *Client) bootstrap(ctx context.Context, method, path string, body any) (map[string]any, error) {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, fault("protocol", "bootstrap", "invalid request body")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, fault("protocol", "bootstrap", "invalid request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://muse.ai")
	req.Header.Set("Referer", "https://muse.ai/thread/new")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	for k, v := range c.Cookies() {
		ck := &http.Cookie{Name: k, Value: v}
		if ck.Valid() != nil {
			return nil, fault("auth", "bootstrap", "invalid cookie format")
		}
		req.AddCookie(ck)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, contextError("bootstrap", ctx.Err())
	}
	defer resp.Body.Close()
	c.mu.Lock()
	for _, ck := range resp.Cookies() {
		if ck.Name != "hatch_gw" && ck.Name != "hatch_sess" && ck.Name != "hatch_vml" {
			continue
		}
		if ck.MaxAge < 0 || (!ck.Expires.IsZero() && ck.Expires.Before(time.Now())) {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck.Value
		}
	}
	c.mu.Unlock()
	if resp.StatusCode != 200 {
		return nil, statusError("bootstrap", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return nil, contextError("bootstrap", ctx.Err())
	}
	if len(raw) > 1<<20 {
		return nil, fault("protocol", "bootstrap", "bootstrap body exceeds limit")
	}
	var result map[string]any
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, fault("protocol", "bootstrap", "invalid bootstrap JSON")
	}
	return result, nil
}
func text(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func object(v any) map[string]any            { m, _ := v.(map[string]any); return m }
func (c *Client) session(ctx context.Context) (map[string]any, error) {
	s, err := c.bootstrap(ctx, "GET", "/api/session?vm_state=live", nil)
	if err == nil {
		c.mu.Lock()
		c.vmState = text(s, "vm_state")
		c.status = text(s, "status")
		c.mu.Unlock()
	}
	return s, err
}
func ready(s map[string]any) bool {
	v := strings.ToUpper(text(s, "vm_state"))
	return text(s, "status") == "assigned" && text(s, "endpoint_url") != "" && text(s, "vm_id") != "" && (v == "" || v == "RUNNING")
}
func (c *Client) connectOnce(ctx context.Context) (*connection, error) {
	session, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	if !ready(session) {
		if text(session, "vm_id") == "" {
			return nil, fault("pending_vm", "connect", "workspace is not assigned")
		}
		_, wakeErr := c.bootstrap(ctx, "POST", "/api/hatch/vm/wake", map[string]any{"vm_id": text(session, "vm_id"), "retry_count": 0, "connect_attempt_id": newID()})
		var he *Error
		if errors.As(wakeErr, &he) && (he.Kind == "auth" || he.Kind == "rate_limit") {
			return nil, wakeErr
		}
		for i := 0; i < 3; i++ {
			if i > 0 {
				if err = delay(ctx, time.Second); err != nil {
					return nil, err
				}
			}
			session, err = c.session(ctx)
			if err != nil {
				return nil, err
			}
			if ready(session) {
				break
			}
		}
		if !ready(session) {
			return nil, fault("pending_vm", "connect", "workspace is not running yet")
		}
	}
	if strings.Contains(strings.ToLower(text(session, "vm_type")), "confidential") {
		return nil, fault("unsupported", "connect", "confidential VM attestation is not supported")
	}
	cvm := object(session["cvm_eligibility"])
	if cvm == nil {
		cvm = object(session["cvmEligibility"])
	}
	state := strings.ToUpper(text(cvm, "state"))
	if state == "" {
		state = strings.ToUpper(text(cvm, "status"))
	}
	if state != "" && state != "NOT_ELIGIBLE" && state != "INELIGIBLE" && state != "NONE" && state != "DISABLED" && state != "N/A" && (strings.Contains(state, "ELIGIB") || state == "REQUIRED" || state == "ENROLLED" || state == "ENABLED") {
		return nil, fault("unsupported", "connect", "confidential VM eligibility is not supported")
	}
	body := map[string]any{"vmAddress": text(session, "endpoint_url")}
	if name := text(session, "vm_name"); name != "" {
		body["vmName"] = name
	}
	token, err := c.bootstrap(ctx, "POST", "/api/hatch/token", body)
	if err != nil {
		return nil, err
	}
	if text(token, "token") == "" {
		return nil, fault("protocol", "connect", "bootstrap returned no token")
	}
	if strings.HasPrefix(text(token, "notary_token"), "delegation.") {
		return nil, fault("unsupported", "connect", "delegation endorsement is not supported")
	}
	u, _ := url.Parse(c.cfg.WSURL)
	q := u.Query()
	q.Set("vm_id", text(session, "vm_id"))
	q.Set("auth_token", text(token, "token"))
	q.Set("app_id", "hatch-web")
	q.Set("request_id", newID())
	if v := text(token, "notary_token"); v != "" {
		q.Set("notary_token", v)
	}
	u.RawQuery = q.Encode()
	hdr := http.Header{"Origin": {"https://muse.ai"}, "User-Agent": {userAgent}, "Accept-Language": {"en-US,en-GB;q=0.9,en;q=0.8"}, "Cache-Control": {"no-cache"}, "Pragma": {"no-cache"}}
	ws, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: hdr, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if resp != nil && resp.StatusCode != 101 {
			return nil, statusError("handshake", resp.StatusCode)
		}
		return nil, contextError("handshake", ctx.Err())
	}
	success := false
	defer func() {
		if !success {
			_ = ws.CloseNow()
		}
	}()
	ws.SetReadLimit(1 << 20)
	suite := noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256)
	static, err := suite.GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, fault("protocol", "handshake", "key generation failed")
	}
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Initiator: true, StaticKeypair: static})
	if err != nil {
		return nil, fault("protocol", "handshake", "Noise initialization failed")
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, fault("protocol", "handshake", "random source failed")
	}
	msg, _, _, err := hs.WriteMessage(nil, putBytes(nil, 1, nonce[:]))
	if err != nil {
		return nil, fault("protocol", "handshake", "Noise message 1 failed")
	}
	if err = ws.Write(ctx, websocket.MessageBinary, msg); err != nil {
		return nil, contextError("handshake", ctx.Err())
	}
	typ, msg, err := ws.Read(ctx)
	if err != nil {
		return nil, contextError("handshake", ctx.Err())
	}
	if typ != websocket.MessageBinary || len(msg) < 96 {
		return nil, fault("protocol", "handshake", "invalid Noise message 2")
	}
	if _, _, _, err = hs.ReadMessage(nil, msg); err != nil {
		return nil, fault("protocol", "handshake", "Noise authentication failed")
	}
	// Standard VM message 2 may include an RV blob. Reference sends an empty
	// message 3; confidential/delegation flows are rejected before connecting.
	msg, tx, rx, err := hs.WriteMessage(nil, nil)
	if err != nil || tx == nil || rx == nil {
		return nil, fault("protocol", "handshake", "Noise message 3 failed")
	}
	if err = ws.Write(ctx, websocket.MessageBinary, msg); err != nil {
		return nil, contextError("handshake", ctx.Err())
	}
	connCtx, cancel := context.WithCancel(c.life)
	cn := &connection{ws: ws, tx: tx, rx: rx, ctx: connCtx, cancel: cancel, dead: make(chan struct{}), streams: make(map[uint64]*stream), next: 1, sendGate: make(chan struct{}, 1)}
	cn.sendGate <- struct{}{}
	success = true
	go cn.receive()
	go cn.heartbeat()
	return cn, nil
}
func delay(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return contextError("connect", ctx.Err())
	case <-t.C:
		return nil
	}
}

// Connect establishes a standard VM transport. Network failures reconnect with
// bounded exponential backoff; requests already sent are never replayed.
func (c *Client) Connect(ctx context.Context) error {
	ctx, cancel := c.operation(ctx, 45*time.Second)
	defer cancel()
	if err := acquire(ctx, c.connectGate); err != nil {
		return err
	}
	defer func() { c.connectGate <- struct{}{} }()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fault("network", "connect", "client is closed")
	}
	cn := c.conn
	c.mu.Unlock()
	if c.initErr != nil {
		return c.initErr
	}
	if cn != nil {
		select {
		case <-cn.dead:
		default:
			return nil
		}
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err = delay(ctx, time.Duration(1<<uint(attempt-1))*200*time.Millisecond); err != nil {
				return err
			}
		}
		cn, err = c.connectOnce(ctx)
		if err == nil {
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				cn.fail(fault("network", "connect", "client is closed"))
				return fault("network", "connect", "client is closed")
			}
			c.conn = cn
			c.mu.Unlock()
			return nil
		}
		var he *Error
		if !errors.As(err, &he) || he.Kind != "network" || ctx.Err() != nil {
			return err
		}
	}
	return err
}
func (c *Client) current(ctx context.Context) (*connection, error) {
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.conn == nil {
		return nil, fault("network", "request", "client is closed")
	}
	return c.conn, nil
}
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cn := c.conn
	c.conn = nil
	c.mu.Unlock()
	c.cancel()
	if cn != nil {
		cn.fail(fault("network", "close", "client is closed"))
	}
	return nil
}
func (c *Client) Probe(ctx context.Context) (ProbeResult, error) {
	ctx, cancel := c.operation(ctx, 50*time.Second)
	defer cancel()
	err := c.Connect(ctx)
	if err == nil {
		_, err = c.request(ctx, "POST", "/api/ping", map[string]any{"presence": "active"})
	}
	c.mu.Lock()
	state := c.vmState
	c.mu.Unlock()
	status := "ok"
	if err != nil {
		status = "unknown"
		var he *Error
		if errors.As(err, &he) {
			switch he.Kind {
			case "auth":
				status = "invalid"
			case "pending_vm":
				status = "pending_vm"
			}
		}
	}
	return ProbeResult{Status: status, VMState: state, Quota: nil, Cookies: c.Cookies()}, err
}

// The reference does not establish a wire route or request schema for these
// operations. Unknown upstream capability is never reported as an empty list.
func (c *Client) Stop(ctx context.Context, nodeID string) error {
	if ctx.Err() != nil {
		return contextError("stop", ctx.Err())
	}
	return fault("unsupported", "stop", "remote cancellation schema is unverified; cancel Generate context to stop local delivery")
}
func (c *Client) ListThreads(ctx context.Context) ([]Thread, error) {
	if ctx.Err() != nil {
		return nil, contextError("threads", ctx.Err())
	}
	return nil, fault("unsupported", "threads", "thread listing route is not exposed by the reference implementation")
}
func (c *Client) History(ctx context.Context, nodeID string) ([]Message, error) {
	if ctx.Err() != nil {
		return nil, contextError("history", ctx.Err())
	}
	return nil, fault("unsupported", "history", "remote history route is not exposed by the reference implementation")
}
func (c *Client) Tasks(ctx context.Context, nodeID string) ([]Task, error) {
	if ctx.Err() != nil {
		return nil, contextError("tasks", ctx.Err())
	}
	return nil, fault("unsupported", "tasks", "agent task listing route is not exposed by the reference implementation")
}
