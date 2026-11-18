// Package pool manages encrypted Muse account cookies and per-account clients.
package pool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
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
	LastError  string `json:"last_error,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type accountRecord struct {
	Account
}

type entry struct {
	Account
	cookies map[string]string
	client  *hatch.Client
	busy    bool
}

type Pool struct {
	db       *store.Store
	mu       sync.Mutex
	accounts map[string]*entry
	cursor   uint64
	base     hatch.Config
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
	if a.ID == "" {
		return errors.New("account id is required")
	}
	if a.Status == "" {
		a.Status = "available"
	}
	// Enabled is an explicit state. Callers that create an active account must
	// set it to true; persistence must never silently re-enable a disabled one.
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	cp := cloneMap(cookies)
	if err := p.db.Put(ctx, "account", a.ID, accountRecord{Account: a}, cp); err != nil {
		return err
	}
	p.mu.Lock()
	p.accounts[a.ID] = &entry{Account: a, cookies: cp}
	p.mu.Unlock()
	return nil
}

func (p *Pool) Update(ctx context.Context, id string, args map[string]any) (Account, error) {
	p.mu.Lock()
	e := p.accounts[id]
	if e == nil {
		p.mu.Unlock()
		return Account{}, sql.ErrNoRows
	}
	if v, ok := args["label"].(string); ok {
		e.Label = v
	}
	if v, ok := args["enabled"].(bool); ok {
		e.Enabled = v
	}
	e.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	a := e.Account
	p.mu.Unlock()
	if err := p.db.Put(ctx, "account", id, accountRecord{Account: a}, nil); err != nil {
		return Account{}, err
	}
	return a, nil
}

func (p *Pool) Remove(ctx context.Context, id string) error {
	if err := p.db.Delete(ctx, "account", id); err != nil {
		return err
	}
	p.mu.Lock()
	if e := p.accounts[id]; e != nil && e.client != nil {
		_ = e.client.Close()
	}
	delete(p.accounts, id)
	p.mu.Unlock()
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

func (p *Pool) clientLocked(e *entry) *hatch.Client {
	if e.client == nil {
		cfg := p.base
		cfg.Cookies = cloneMap(e.cookies)
		e.client = hatch.New(cfg)
	}
	return e.client
}

// WithClient reserves one healthy account for the duration of fn. Accounts are
// serialized independently, while different accounts can run concurrently.
func (p *Pool) WithClient(ctx context.Context, fn func(*hatch.Client, Account) error) error {
	for {
		p.mu.Lock()
		var chosen *entry
		ids := make([]string, 0, len(p.accounts))
		for id := range p.accounts {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) == 0 {
			p.mu.Unlock()
			return ErrNoAccount
		}
		if len(ids) > 0 {
			start := int(p.cursor % uint64(len(ids)))
			for i := 0; i < len(ids); i++ {
				e := p.accounts[ids[(start+i)%len(ids)]]
				if e.Enabled && !e.busy && e.Status != "disabled" && e.Status != "dead" {
					chosen = e
					p.cursor++
					break
				}
			}
		}
		if chosen != nil {
			chosen.busy = true
			c := p.clientLocked(chosen)
			a := chosen.Account
			p.mu.Unlock()
			err := fn(c, a)
			p.mu.Lock()
			chosen.busy = false
			chosen.LastUsedAt = time.Now().UTC().Format(time.RFC3339Nano)
			chosen.UpdatedAt = chosen.LastUsedAt
			if err != nil {
				chosen.LastError = err.Error()
				chosen.Status = "error"
			} else {
				chosen.LastError = ""
				if chosen.Status == "error" {
					chosen.Status = "available"
				}
			}
			snap := chosen.Account
			p.mu.Unlock()
			_ = p.db.Put(context.Background(), "account", snap.ID, accountRecord{Account: snap}, nil)
			return err
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (p *Pool) Probe(ctx context.Context, id string) (Account, error) {
	p.mu.Lock()
	e := p.accounts[id]
	if e == nil {
		p.mu.Unlock()
		return Account{}, sql.ErrNoRows
	}
	if !e.Enabled {
		a := e.Account
		p.mu.Unlock()
		return a, nil
	}
	c := p.clientLocked(e)
	p.mu.Unlock()
	_, err := c.Probe(ctx)
	p.mu.Lock()
	if err != nil {
		e.Status = "error"
		e.LastError = err.Error()
	} else {
		e.Status = "available"
		e.LastError = ""
	}
	e.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	snap := e.Account
	p.mu.Unlock()
	_ = p.db.Put(context.Background(), "account", snap.ID, accountRecord{Account: snap}, nil)
	return snap, err
}

func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.accounts {
		if e.client != nil {
			_ = e.client.Close()
			e.client = nil
		}
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

// Kept local to avoid exposing persistence representation as part of pool API.
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
