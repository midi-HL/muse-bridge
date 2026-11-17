package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/midi-HL/muse-bridge/internal/config"
	"github.com/midi-HL/muse-bridge/internal/store"
)

func persistSettings(ctx context.Context, db *store.Store, values map[string]any) error {
	public := map[string]any{}
	secrets := map[string]string{}
	for k, v := range values {
		if f, ok := config.Find(k); ok && f.Secret {
			if text, ok := v.(string); ok && text != "" {
				secrets[k] = text
			}
			continue
		}
		public[k] = v
	}
	return db.Put(ctx, "settings", "current", public, secrets)
}

func publicSettings(values map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range values {
		if f, ok := config.Find(k); ok && f.Secret {
			text, _ := v.(string)
			out[k] = map[string]any{"configured": text != "", "masked": func() string {
				if text != "" {
					return "••••••••"
				}
				return ""
			}()}
			continue
		}
		out[k] = v
	}
	return out
}

type accountImport struct {
	ID      string
	Label   string
	Cookies map[string]string
}

func parseAccountImports(text string) ([]accountImport, error) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var rows []map[string]any
		if strings.HasPrefix(trimmed, "{") {
			var one map[string]any
			if err := json.Unmarshal([]byte(trimmed), &one); err != nil {
				return nil, errors.New("invalid account JSON")
			}
			if list, ok := one["accounts"].([]any); ok {
				for _, raw := range list {
					if row, ok := raw.(map[string]any); ok {
						rows = append(rows, row)
					}
				}
			} else {
				rows = []map[string]any{one}
			}
		} else if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
			return nil, errors.New("invalid account JSON")
		}
		out := make([]accountImport, 0, len(rows))
		for _, row := range rows {
			cookies := map[string]string{}
			if nested, ok := row["cookies"].(map[string]any); ok {
				for k, v := range nested {
					if str, ok := v.(string); ok {
						cookies[k] = str
					}
				}
			}
			for _, key := range []string{"hatch_gw", "hatch_sess", "hatch_vml"} {
				if value, ok := row[key].(string); ok {
					cookies[key] = value
				}
			}
			if cookies["hatch_sess"] == "" {
				return nil, errors.New("missing required hatch_sess cookie")
			}
			id, _ := row["id"].(string)
			label, _ := row["label"].(string)
			out = append(out, accountImport{ID: id, Label: label, Cookies: cookies})
		}
		if len(out) == 0 {
			return nil, errors.New("no accounts found")
		}
		return out, nil
	}
	cookies, err := parseCookieText(trimmed)
	if err != nil {
		return nil, err
	}
	return []accountImport{{Cookies: cookies}}, nil
}

func parseCookieText(text string) (map[string]string, error) {
	cookies := map[string]string{}
	for _, part := range strings.Split(strings.TrimSpace(text), ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k != "" {
			cookies[k] = v
		}
	}
	if cookies["hatch_sess"] == "" {
		return nil, errors.New("missing required hatch_sess cookie")
	}
	return cookies, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
