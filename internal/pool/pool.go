// Package pool manages encrypted Muse account cookies and per-account clients.
package pool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/store"
)

var ErrNoAccount = errors.New("account pool is empty")

type Account struct {
	ID         string `json:"id"`
	Label      string `json:"label,omitempty"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	NodeID     string `json:"node_id"`
	Pool       string `json:"pool"`
	Note       string `json:"note"`
	LastError  string `json:"last_error,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type accountRecord struct {
	Account
}

type entry struct {
	Account
	cookies        map[string]string
	client         *hatch.Client
	clientKey      string
	refreshPending bool
	busy           bool
	removed        bool
	generation     uint64
}

type Pool struct {
	db       *store.Store
	mu       sync.Mutex
	accounts map[string]*entry
	cursor   uint64
	base     hatch.Config
	closed   bool
}

func New(db *store.Store, cfg hatch.Config) (*Pool, error) {
	if db == nil {
		return nil, errors.New("pool: store is required")
	}
	p := &Pool{db: db, accounts: map[string]*entry{}, base: cfg}
	rows, err := db.List(context.Background(), "account")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var rec accountRecord
		if err := jsonUnmarshal(row.Data, &rec); err != nil {
			continue
		}
		if rec.ID == "" {
			rec.ID = row.ID
		}
		if rec.Pool == "" {
			rec.Pool = "shared"
		}
		cookies := map[string]string{}
		if err := db.Secret(context.Background(), "account", rec.ID, &cookies); err != nil && !errors.Is(err, sql.ErrNoRows) {
			continue
		}
		p.accounts[rec.ID] = &entry{Account: rec.Account, cookies: cookies}
	}
	return p, nil
}

// Add persists metadata publicly and cookies in the store's encrypted secret column.
func (p *Pool) Add(ctx context.Context, a Account, cookies map[string]string) error {
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("account id is required")
	}
	if a.Pool == "" {
		a.Pool = "shared"
	}
	if !validPool(a.Pool) {
		return errors.New("pool is invalid")
	}
	if a.Status == "" {
		a.Status = "available"
	}
	if err := p.validateNode(ctx, a.NodeID); err != nil {
		return err
	}
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	cp := cloneMap(cookies)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("pool is closed")
	}
	if old := p.accounts[a.ID]; old != nil {
		if err := p.db.Put(ctx, "account", a.ID, accountRecord{Account: a}, cp); err != nil {
			return err
		}
		old.Account = a
		old.cookies = cp
		old.generation++
		p.refreshClientLocked(old)
		return nil
	}
	if err := p.db.Put(ctx, "account", a.ID, accountRecord{Account: a}, cp); err != nil {
		return err
	}
	p.accounts[a.ID] = &entry{Account: a, cookies: cp}
	return nil
}

func (p *Pool) Update(ctx context.Context, id string, args map[string]any) (Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.accounts[id]
	if p.closed {
		return Account{}, errors.New("pool is closed")
	}
	if e == nil || e.removed {
		return Account{}, sql.ErrNoRows
	}
	next, err := accountPatch(e.Account, id, args)
	if err != nil {
		return Account{}, err
	}
	if next.NodeID != e.NodeID {
		if err := p.validateNode(ctx, next.NodeID); err != nil {
			return Account{}, err
		}
	}
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := p.db.Put(ctx, "account", id, accountRecord{Account: next}, nil); err != nil {
		return Account{}, errors.New("account update could not be persisted")
	}
	routeChanged := e.NodeID != next.NodeID
	e.Account = next
	e.generation++
	if routeChanged {
		p.refreshClientLocked(e)
	}
	return next, nil
}

func validPool(name string) bool { return name == "shared" || name == "chat" || name == "api" }

func validateProxyURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	case "socks5", "socks5h":
		if u.Port() == "" {
			return false
		}
	default:
		return false
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(raw, "\x00\r\n") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func (p *Pool) nodeURL(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	var node struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := p.db.Get(ctx, "node", id, &node); err != nil || !node.Enabled {
		return "", errors.New("node is unavailable")
	}
	var secret map[string]string
	if err := p.db.Secret(ctx, "node", id, &secret); err != nil || !validateProxyURL(secret["url"]) {
		return "", errors.New("node is unavailable")
	}
	return secret["url"], nil
}

func (p *Pool) validateNode(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	if _, err := p.nodeURL(ctx, id); err != nil {
		return errors.New("node is unavailable")
	}
	return nil
}

func (p *Pool) Remove(ctx context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("pool is closed")
	}
	e := p.accounts[id]
	if e == nil {
		return sql.ErrNoRows
	}
	if err := p.db.Delete(ctx, "account", id); err != nil {
		return err
	}
	e.removed = true
	e.generation++
	if e.client != nil {
		_ = e.client.Close()
		e.client = nil
	}
	delete(p.accounts, id)
	return nil
}

func (p *Pool) List() []Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Account, 0, len(p.accounts))
	for _, e := range p.accounts {
		out = append(out, e.Account)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (p *Pool) Count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.accounts) }

// Route/credential changes take effect on the next lease. Never cancel the
// request holding the current lease just because its account was edited.
func (p *Pool) refreshClientLocked(e *entry) {
	if e.busy {
		e.refreshPending = true
		return
	}
	p.invalidateClientLocked(e)
}

func (p *Pool) releaseClientLocked(e *entry, c *hatch.Client) {
	if !e.refreshPending && e.client == c {
		if cookies := c.Cookies(); len(cookies) > 0 {
			e.cookies = cloneMap(cookies)
		}
	}
	e.busy = false
	if e.refreshPending {
		p.invalidateClientLocked(e)
	}
}

func (p *Pool) invalidateClientLocked(e *entry) {
	if e.client != nil {
		_ = e.client.Close()
		e.client = nil
	}
	e.clientKey = ""
	e.refreshPending = false
}

func (p *Pool) clientLocked(ctx context.Context, e *entry) (*hatch.Client, error) {
	proxyURL, err := p.nodeURL(ctx, e.NodeID)
	if err != nil {
		p.invalidateClientLocked(e)
		return nil, err
	}
	if p.closed {
		return nil, errors.New("pool is closed")
	}
	key := e.NodeID + "\x00" + proxyURL
	if e.client != nil && e.clientKey != key {
		p.invalidateClientLocked(e)
	}
	if e.client == nil {
		cfg := p.base
		cfg.Cookies = cloneMap(e.cookies)
		if e.NodeID != "" {
			cfg.ProxyURL = proxyURL
		}
		e.client = hatch.New(cfg)
		e.clientKey = key
	}
	return e.client, nil
}

// WithClient reserves one healthy account for the duration of fn.
func (p *Pool) WithClient(ctx context.Context, fn func(*hatch.Client, Account) error) error {
	return p.WithClientFor(ctx, "", false, fn)
}

// WithClientFor selects by platform when split routing is enabled. api uses
// api/shared pools; web and telegram use chat/shared pools.
func (p *Pool) WithClientFor(ctx context.Context, platform string, split bool, fn func(*hatch.Client, Account) error) error {
	for {
		p.mu.Lock()
		if ctx.Err() != nil {
			p.mu.Unlock()
			return ctx.Err()
		}
		if p.closed {
			p.mu.Unlock()
			return errors.New("pool is closed")
		}
		ids := make([]string, 0, len(p.accounts))
		eligible := false
		for id, e := range p.accounts {
			if accountEligible(e, platform, split) {
				eligible = true
				ids = append(ids, id)
			}
		}
		if !eligible {
			p.mu.Unlock()
			return ErrNoAccount
		}
		sort.Strings(ids)
		var chosen *entry
		var c *hatch.Client
		var clientErr error
		busySeen := false
		start := int(p.cursor % uint64(len(ids)))
		for i := 0; i < len(ids); i++ {
			e := p.accounts[ids[(start+i)%len(ids)]]
			if e.busy {
				busySeen = true
				continue
			}
			candidate, err := p.clientLocked(ctx, e)
			if err != nil {
				clientErr = err
				continue
			}
			chosen, c = e, candidate
			p.cursor++
			break
		}
		if chosen != nil {
			chosen.busy = true
			generation := chosen.generation
			a := chosen.Account
			p.mu.Unlock()
			err := invokeLease(fn, c, a)
			p.finishLease(chosen, generation, c, err)
			return err
		}
		p.mu.Unlock()
		if busySeen {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(25 * time.Millisecond):
				continue
			}
		}
		if clientErr != nil {
			return errors.New("account client unavailable")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func invokeLease(fn func(*hatch.Client, Account) error, client *hatch.Client, account Account) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("account request failed")
		}
	}()
	return fn(client, account)
}

func accountEligible(e *entry, platform string, split bool) bool {
	if e == nil || e.removed || !e.Enabled || e.Status == "disabled" || e.Status == "dead" {
		return false
	}
	if !split || e.Pool == "" || e.Pool == "shared" {
		return true
	}
	switch platform {
	case "api":
		return e.Pool == "api"
	case "web", "telegram":
		return e.Pool == "chat"
	default:
		return false
	}
}

// upstreamAccountFailure stores only a safe, translatable diagnostic code.
func upstreamAccountFailure(err error) (status, message string) {
	var protocolErr *hatch.Error
	if errors.As(err, &protocolErr) {
		switch protocolErr.Kind {
		case "auth":
			return "invalid", "upstreamAuthFailed"
		case "pending_vm":
			return "pending_vm", "workspaceNotRunning"
		case "network":
			return "error", "probeNetworkFailed"
		case "rate_limit":
			return "error", "upstreamRateLimited"
		case "unsupported":
			return "error", "unsupported"
		case "protocol":
			return "error", "probeProtocolFailed"
		}
	}
	return "error", "accountProbeFailed"
}

func (p *Pool) finishLease(e *entry, generation uint64, c *hatch.Client, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.accounts[e.ID] != e || e.removed {
		return
	}
	if e.generation != generation {
		p.releaseClientLocked(e, c)
		_ = p.db.Put(context.Background(), "account", e.ID, accountRecord{Account: e.Account}, cloneMap(e.cookies))
		return
	}
	p.releaseClientLocked(e, c)
	e.LastUsedAt = time.Now().UTC().Format(time.RFC3339Nano)
	e.UpdatedAt = e.LastUsedAt
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		e.Status, e.LastError = upstreamAccountFailure(err)
	} else if err == nil {
		e.LastError = ""
		if e.Enabled {
			e.Status = "available"
		}
	}
	snap := e.Account
	secret := cloneMap(e.cookies)
	_ = p.db.Put(context.Background(), "account", snap.ID, accountRecord{Account: snap}, secret)
}

func (p *Pool) Probe(ctx context.Context, id string) (Account, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return Account{}, errors.New("pool is closed")
		}
		e := p.accounts[id]
		if e == nil || e.removed {
			p.mu.Unlock()
			return Account{}, sql.ErrNoRows
		}
		if !e.Enabled {
			a := e.Account
			p.mu.Unlock()
			return a, nil
		}
		if !e.busy {
			c, err := p.clientLocked(ctx, e)
			if err != nil {
				snap := e.Account
				p.mu.Unlock()
				return snap, errors.New("account client unavailable")
			}
			e.busy = true
			generation := e.generation
			a := e.Account
			p.mu.Unlock()
			probeResult, probeErr := c.Probe(ctx)
			p.mu.Lock()
			if p.closed || p.accounts[id] != e || e.removed {
				p.mu.Unlock()
				return a, probeErr
			}
			if e.generation != generation {
				p.releaseClientLocked(e, c)
				_ = p.db.Put(context.Background(), "account", e.ID, accountRecord{Account: e.Account}, cloneMap(e.cookies))
				snap := e.Account
				p.mu.Unlock()
				return snap, probeErr
			}
			if errors.Is(probeErr, context.Canceled) || errors.Is(probeErr, context.DeadlineExceeded) {
				p.releaseClientLocked(e, c)
				snap := e.Account
				p.mu.Unlock()
				return snap, probeErr
			}
			if probeErr != nil {
				e.Status, e.LastError = upstreamAccountFailure(probeErr)
			} else {
				e.Status = probeResult.Status
				if e.Status == "" || e.Status == "ok" {
					e.Status = "available"
				}
				e.LastError = ""
			}
			e.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			p.releaseClientLocked(e, c)
			snap, cookies := e.Account, cloneMap(e.cookies)
			_ = p.db.Put(context.Background(), "account", snap.ID, accountRecord{Account: snap}, cookies)
			p.mu.Unlock()
			return snap, probeErr
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return Account{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	for _, e := range p.accounts {
		p.invalidateClientLocked(e)
	}
	return nil
}

func cloneMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
