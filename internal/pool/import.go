package pool

import (
	"context"
	"errors"
	"strings"
	"time"
)

func validAccountID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.:-", r)) {
			return false
		}
	}
	return true
}

// accountPatch applies only explicitly supplied metadata. Cookie refresh and
// ordinary updates use the same validation; no partial mutation reaches storage.
func accountPatch(next Account, id string, args map[string]any) (Account, error) {
	for key, raw := range args {
		text, isText := raw.(string)
		switch key {
		case "id":
			if !isText || text != id {
				return Account{}, errors.New("account id does not match")
			}
		case "enabled":
			value, ok := raw.(bool)
			if !ok {
				return Account{}, errors.New("enabled must be boolean")
			}
			next.Enabled = value
			if value && next.Status == "disabled" {
				next.Status = "unknown"
			}
		case "label", "node_id", "pool", "note":
			if !isText || strings.ContainsAny(text, "\x00\r") || len(text) > 32768 {
				return Account{}, errors.New("invalid account metadata")
			}
			switch key {
			case "label":
				next.Label = text
			case "node_id":
				next.NodeID = text
			case "pool":
				next.Pool = text
			case "note":
				next.Note = text
			}
		default:
			return Account{}, errors.New("account update field is not supported")
		}
	}
	if next.Pool == "" {
		next.Pool = "shared"
	}
	if !validPool(next.Pool) || (next.NodeID != "" && !validAccountID(next.NodeID)) {
		return Account{}, errors.New("invalid account routing metadata")
	}
	return next, nil
}

// Import refreshes cookies and optionally patches metadata in one pool critical
// section. In particular, refreshing credentials never unbinds a proxy, enables
// a disabled account or changes its traffic pool unless explicitly requested.
func (p *Pool) Import(ctx context.Context, id string, fields map[string]any, cookies map[string]string) (Account, error) {
	if !validAccountID(id) {
		return Account{}, errors.New("invalid account id")
	}
	if strings.TrimSpace(cookies["hatch_sess"]) == "" {
		return Account{}, errors.New("missing required hatch_sess cookie")
	}
	for _, value := range cookies {
		if strings.ContainsAny(value, "\x00\r\n") {
			return Account{}, errors.New("invalid cookie value")
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return Account{}, errors.New("pool is closed")
	}
	old := p.accounts[id]
	label := id
	if len(label) > 8 {
		label = label[:8]
	}
	next := Account{ID: id, Label: label, Enabled: true, Status: "unknown", Pool: "shared"}
	if old != nil {
		next = old.Account
	}
	next, err := accountPatch(next, id, fields)
	if err != nil {
		return Account{}, err
	}
	if old == nil || next.NodeID != old.NodeID {
		if err = p.validateNode(ctx, next.NodeID); err != nil {
			return Account{}, err
		}
	}
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	copyCookies := cloneMap(cookies)
	if err = p.db.Put(ctx, "account", id, accountRecord{Account: next}, copyCookies); err != nil {
		return Account{}, errors.New("account import could not be persisted")
	}
	if old != nil {
		old.Account = next
		old.cookies = copyCookies
		old.generation++
		p.refreshClientLocked(old)
	} else {
		p.accounts[id] = &entry{Account: next, cookies: copyCookies}
	}
	return next, nil
}
