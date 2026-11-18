package core

import "context"

func (s *Service) protocolProbe(ctx context.Context, args map[string]any) (map[string]any, error) {
	id, _ := args["id"].(string)
	if id != "" {
		a, err := s.pool.Probe(ctx, id)
		return map[string]any{"account": a, "checked": true}, err
	}
	accounts := s.pool.List()
	if len(accounts) == 0 {
		return map[string]any{"status": "empty_pool", "checked": 0, "capabilities": []string{"chat.stream", "files.upload", "files.download"}}, nil
	}
	checked := 0
	healthy := 0
	var lastErr error
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		checked++
		got, err := s.pool.Probe(ctx, a.ID)
		if err == nil && got.Status != "error" {
			healthy++
		} else {
			lastErr = err
		}
	}
	out := map[string]any{"status": "ok", "checked": checked, "healthy": healthy, "capabilities": []string{"chat.stream", "files.upload", "files.download", "heartbeat"}, "remote_tasks": "unknown", "remote_threads": "unknown", "remote_quota": "unknown", "remote_cancel": "unsupported"}
	if healthy == 0 {
		out["status"] = "unavailable"
		if lastErr != nil {
			out["error"] = "account probe failed"
		}
	}
	return out, nil
}
