package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
	"strings"
	"sync"
	"time"
)

type APIKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mask      string `json:"masked"`
	RPM       int    `json:"rpm"`
	Daily     int    `json:"daily"`
	Revoked   bool   `json:"revoked"`
	CreatedAt string `json:"created_at"`
	TotalUsed int    `json:"total_used"`
	TodayUsed int    `json:"today_used"`
}
type keySecret struct {
	Hash string `json:"hash"`
}
type Keys struct {
	db       *store.Store
	mu       sync.Mutex
	redactor *secure.Redactor
	Timezone func() string
	now      func() time.Time
}

func NewKeys(db *store.Store, r *secure.Redactor) *Keys { return &Keys{db: db, redactor: r} }
func (k *Keys) currentTime() time.Time {
	if k.now != nil {
		return k.now()
	}
	return time.Now()
}

// SetClockForTesting stabilizes RPM/day counter boundary tests.
func (k *Keys) SetClockForTesting(now func() time.Time) { k.now = now }

func (k *Keys) day() string {
	loc := time.UTC
	if k.Timezone != nil {
		if l, e := time.LoadLocation(k.Timezone()); e == nil {
			loc = l
		}
	}
	return k.currentTime().In(loc).Format("2006-01-02")
}
func (k *Keys) List(ctx context.Context) ([]APIKey, error) {
	rows, e := k.db.List(ctx, "api_key")
	if e != nil {
		return nil, e
	}
	out := []APIKey{}
	for _, r := range rows {
		var item APIKey
		if e = json.Unmarshal(r.Data, &item); e != nil {
			return nil, e
		}
		item.TotalUsed = k.db.Count(ctx, "api_total", item.ID, "all")
		item.TodayUsed = k.db.Count(ctx, "api_day", item.ID, k.day())
		out = append(out, item)
	}
	return out, nil
}
func (k *Keys) Validate(ctx context.Context, token string) (APIKey, error) {
	return k.authenticate(ctx, token)
}

func (k *Keys) Action(ctx context.Context, name string, args map[string]any) (any, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	id := stringArg(args, "id")
	switch name {
	case "keys.create":
		name := strings.TrimSpace(stringArg(args, "name"))
		if name == "" || len(name) > 100 {
			return nil, errors.New("name must contain 1–100 bytes")
		}
		rpm, daily, e := keyLimits(args)
		if e != nil {
			return nil, e
		}
		id = secure.ID()
		token := "mbk_" + id + "." + secure.Random(32)
		item := APIKey{ID: id, Name: name, Mask: "mbk_••••••••", RPM: rpm, Daily: daily, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
		if e = k.db.Put(ctx, "api_key", id, item, keySecret{secure.Digest(token)}); e != nil {
			return nil, e
		}
		k.redactor.Add(token)
		return map[string]any{"key": token, "id": id, "name": name}, nil
	case "keys.revoke", "keys.update":
		var item APIKey
		if e := k.db.Get(ctx, "api_key", id, &item); e != nil {
			return nil, errors.New("API key not found")
		}
		if name == "keys.revoke" {
			item.Revoked = true
		} else {
			if _, hasRPM := args["rpm"]; hasRPM {
				rpm, _, e := keyLimits(map[string]any{"rpm": args["rpm"], "daily": float64(item.Daily)})
				if e != nil {
					return nil, e
				}
				item.RPM = rpm
			}
			if _, hasDaily := args["daily"]; hasDaily {
				_, daily, e := keyLimits(map[string]any{"rpm": float64(item.RPM), "daily": args["daily"]})
				if e != nil {
					return nil, e
				}
				item.Daily = daily
			}
		}
		return item, k.db.Put(ctx, "api_key", id, item, nil)
	}
	return nil, errors.New("unknown API key operation")
}
func keyLimits(args map[string]any) (int, int, error) {
	rpm, daily := 60, 1000
	for name, dest := range map[string]*int{"rpm": &rpm, "daily": &daily} {
		if v, ok := args[name]; ok {
			n, ok := v.(float64)
			if !ok || n < 0 || n > 1000000 || n != float64(int(n)) {
				return 0, 0, errors.New("rpm and daily must be integers from 0 to 1000000")
			}
			*dest = int(n)
		}
	}
	return rpm, daily, nil
}
func (k *Keys) Authenticate(ctx context.Context, token string, charge bool) (APIKey, error) {
	item, err := k.authenticate(ctx, token)
	if err != nil {
		return APIKey{}, err
	}
	if charge {
		if err := k.Charge(ctx, item); err != nil {
			return APIKey{}, err
		}
	}
	return item, nil
}

func (k *Keys) authenticate(ctx context.Context, token string) (APIKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(token) > 256 || !strings.HasPrefix(token, "mbk_") {
		return APIKey{}, errors.New("invalid API key")
	}
	parts := strings.Split(strings.TrimPrefix(token, "mbk_"), ".")
	if len(parts) != 2 {
		return APIKey{}, errors.New("invalid API key")
	}
	var item APIKey
	var sec keySecret
	if k.db.Get(ctx, "api_key", parts[0], &item) != nil || item.Revoked || k.db.Secret(ctx, "api_key", parts[0], &sec) != nil {
		return APIKey{}, errors.New("invalid API key")
	}
	if subtle.ConstantTimeCompare([]byte(sec.Hash), []byte(secure.Digest(token))) != 1 {
		return APIKey{}, errors.New("invalid API key")
	}
	k.redactor.Add(token)
	return item, nil
}

func (k *Keys) Charge(ctx context.Context, item APIKey) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	var current APIKey
	var secret keySecret
	if err := k.db.Get(ctx, "api_key", item.ID, &current); err != nil || current.Revoked || k.db.Secret(ctx, "api_key", item.ID, &secret) != nil {
		return errors.New("invalid API key")
	}
	if _, err := k.db.Increment(ctx, "api_minute", item.ID, k.currentTime().UTC().Format("2006-01-02T15:04"), current.RPM); err != nil {
		return err
	}
	if _, err := k.db.Increment(ctx, "api_day", item.ID, k.day(), current.Daily); err != nil {
		return err
	}
	if _, err := k.db.Increment(ctx, "api_total", item.ID, "all", 0); err != nil {
		return err
	}
	return nil
}
func stringArg(m map[string]any, k string) string { s, _ := m[k].(string); return s }
