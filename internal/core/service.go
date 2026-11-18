// Package core exposes the runtime service shared by HTTP and CLI adapters.
package core

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/midi-HL/muse-bridge/internal/config"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/media"
	"github.com/midi-HL/muse-bridge/internal/pool"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

type Service struct {
	life             context.Context
	cancel           context.CancelFunc
	db               *store.Store
	vault            *secure.Vault
	redactor         *secure.Redactor
	dataDir          string
	settingsMu       sync.RWMutex
	settings         map[string]any
	settingsHook     func(map[string]any) error
	pool             *pool.Pool
	media            *media.Processor
	gate             chan struct{}
	queueMu          sync.Mutex
	queued           int
	sessionsMu       sync.Mutex
	sessions         map[string]context.CancelFunc
	scheduleMu       sync.Mutex
	scheduleCallback ScheduleCallback
	scheduleRuns     map[string]bool
	schedulerWG      sync.WaitGroup
	executionWG      sync.WaitGroup
	closeOnce        sync.Once
}

type fileRecord struct {
	File
	Path string `json:"path"`
}

type sessionRecord struct {
	ID        string          `json:"id"`
	Platform  string          `json:"platform"`
	UserID    string          `json:"user_id"`
	Title     string          `json:"title"`
	Messages  []hatch.Message `json:"messages"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

func New(ctx context.Context, db *store.Store, vault *secure.Vault, redactor *secure.Redactor, dataDir string, overrides map[string]any) (*Service, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return nil, errors.New("core: store is required")
	}
	if vault == nil {
		return nil, errors.New("core: vault is required")
	}
	if dataDir == "" {
		dataDir = "."
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "files"), 0700); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(ctx)
	settings := config.Defaults()
	// Settings are public metadata in the database; encrypted credentials remain
	// owned by the account/admin stores and are never copied into API responses.
	var persisted map[string]any
	if err := db.Get(ctx, "settings", "current", &persisted); err == nil {
		for k, v := range persisted {
			if f, ok := config.Find(k); ok && !f.Secret {
				settings[k] = normalizeSetting(v)
			}
		}
	}
	var secrets map[string]string
	if err := db.Secret(ctx, "settings", "current", &secrets); err == nil {
		for k, v := range secrets {
			if f, ok := config.Find(k); ok && f.Secret {
				settings[k] = v
			}
		}
	}
	for k, v := range overrides {
		if _, ok := config.Find(k); ok {
			settings[k] = normalizeSetting(v)
		}
	}
	if err := config.Validate(settings); err != nil {
		cancel()
		return nil, err
	}
	if err := persistSettings(ctx, db, settings); err != nil {
		cancel()
		return nil, err
	}
	concurrency := settingInt(settings, "limits.concurrency", 2)
	if concurrency < 1 {
		concurrency = 1
	}
	base := hatch.Config{}
	if v, ok := overrides["hatch.base_url"].(string); ok {
		base.BaseURL = v
	}
	if v, ok := overrides["hatch.ws_url"].(string); ok {
		base.WSURL = v
	}
	if v, ok := overrides["hatch.proxy_url"].(string); ok {
		base.ProxyURL = v
	}
	p, err := pool.New(db, base)
	if err != nil {
		cancel()
		return nil, err
	}
	mp, err := media.New(settings, filepath.Join(dataDir, "media"))
	if err != nil {
		cancel()
		_ = p.Close()
		return nil, err
	}
	s := &Service{life: life, cancel: cancel, db: db, vault: vault, redactor: redactor, dataDir: dataDir, settings: settings, pool: p, media: mp, gate: make(chan struct{}, concurrency), sessions: map[string]context.CancelFunc{}, scheduleRuns: map[string]bool{}}
	s.startScheduler()
	return s, nil
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.sessionsMu.Lock()
		for id, c := range s.sessions {
			c()
			delete(s.sessions, id)
		}
		s.sessionsMu.Unlock()
		s.schedulerWG.Wait()
		s.executionWG.Wait()
		_ = s.pool.Close()
	})
	return nil
}
func (s *Service) Settings() map[string]any {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return publicSettings(s.settings)
}
func (s *Service) Schema() map[string]any {
	return map[string]any{"settings": append([]config.Field(nil), config.Fields...), "operations": []string{
		"state", "schema", "settings", "settings.update", "log",
		"accounts", "accounts.list", "accounts.import", "accounts.update", "accounts.delete", "accounts.probe",
		"nodes.save", "nodes.delete", "nodes.test", "nodes.probe",
		"personas.save", "personas.delete",
		"schedules.save", "schedules.delete",
		"stickers.import", "stickers.update", "stickers.delete",
		"backup.export", "backup.import",
		"asr.test", "asr.download",
		"sessions.history", "sessions.reset", "sessions.rename", "sessions.export", "chat.stop",
	}}
}
func (s *Service) SetSettingsHook(fn func(map[string]any) error) {
	s.settingsMu.Lock()
	s.settingsHook = fn
	s.settingsMu.Unlock()
}
func (s *Service) State(ctx context.Context) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := s.db.List(ctx, "session")
	if err != nil {
		return nil, err
	}
	sessions := make([]map[string]any, 0, len(records))
	for _, r := range records {
		var item sessionRecord
		if json.Unmarshal(r.Data, &item) == nil {
			item.Messages = nil
			sessions = append(sessions, map[string]any{"id": item.ID, "platform": item.Platform, "user_id": item.UserID, "title": item.Title, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt})
		}
	}
	nodes, err := s.listNodes(ctx)
	if err != nil {
		return nil, err
	}
	personas, err := s.listManaged(ctx, "persona")
	if err != nil {
		return nil, err
	}
	stickers, err := s.listManaged(ctx, "sticker")
	if err != nil {
		return nil, err
	}
	schedules, err := s.listManaged(ctx, "schedule")
	if err != nil {
		return nil, err
	}
	return map[string]any{"accounts": s.pool.List(), "account_count": s.pool.Count(), "queue_depth": s.queueDepth(), "settings": s.Settings(), "sessions": sessions, "nodes": nodes, "personas": personas, "stickers": stickers, "schedules": schedules, "logs": func() any { v, _ := s.db.Logs(ctx); return v }()}, nil
}

func (s *Service) Action(ctx context.Context, name string, args map[string]any) (any, error) {
	switch strings.ToLower(name) {
	case "state":
		return s.State(ctx)
	case "settings":
		return s.Settings(), nil
	case "schema":
		return s.Schema(), nil
	case "log":
		return nil, s.Log(ctx, fmt.Sprint(args["level"]), fmt.Sprint(args["message"]))
	case "settings.update":
		vals := map[string]any{}
		if nested, ok := args["values"].(map[string]any); ok {
			args = nested
		}
		for k, v := range args {
			vals[k] = normalizeSetting(v)
		}
		if err := config.Validate(vals); err != nil {
			return nil, err
		}
		s.settingsMu.Lock()
		previous := cloneAnyMap(s.settings)
		for k, v := range vals {
			s.settings[k] = v
		}
		out := cloneAnyMap(s.settings)
		hook := s.settingsHook
		s.settingsMu.Unlock()
		if err := persistSettings(ctx, s.db, out); err != nil {
			s.settingsMu.Lock()
			s.settings = previous
			s.settingsMu.Unlock()
			return nil, err
		}
		if hook != nil {
			// Hooks are internal runtime adapters (Telegram reload, schedulers),
			// not API output. They need the raw values to apply secret settings;
			// callers still receive only the masked map below.
			if err := hook(out); err != nil {
				s.settingsMu.Lock()
				s.settings = previous
				s.settingsMu.Unlock()
				_ = persistSettings(ctx, s.db, previous)
				// Best effort to restore any external runtime adapter that may
				// have partially applied the rejected settings.
				_ = hook(previous)
				return nil, err
			}
		}
		return publicSettings(out), nil
	case "accounts", "accounts.list":
		return s.pool.List(), nil
	case "accounts.import", "account.import":
		text, _ := args["text"].(string)
		if text != "" {
			imports, err := parseAccountImports(text)
			if err != nil {
				return nil, err
			}
			added := make([]pool.Account, 0, len(imports))
			for _, item := range imports {
				id := item.ID
				if id == "" {
					id = secure.ID()
				}
				label := item.Label
				if label == "" {
					label = id[:8]
				}
				a := pool.Account{ID: id, Label: label, Enabled: true, Status: "unknown"}
				if err := s.pool.Add(ctx, a, item.Cookies); err != nil {
					return nil, err
				}
				added = append(added, a)
				if s.redactor != nil {
					for _, v := range item.Cookies {
						s.redactor.Add(v)
					}
				}
			}
			if s.redactor != nil {
				s.redactor.Add(text)
			}
			return map[string]any{"count": len(added), "added": added, "failed": []any{}}, nil
		}
		id, _ := args["id"].(string)
		if id == "" {
			id = secure.ID()
		}
		label, _ := args["label"].(string)
		cookies := map[string]string{}
		if raw, ok := args["cookies"].(map[string]any); ok {
			for k, v := range raw {
				if str, ok := v.(string); ok {
					cookies[k] = str
				}
			}
		}
		if raw, ok := args["cookies"].(map[string]string); ok {
			cookies = cloneStringMap(raw)
		}
		if cookies["hatch_sess"] == "" {
			return nil, errors.New("missing required hatch_sess cookie")
		}
		a := pool.Account{ID: id, Label: label, Enabled: true, Status: "unknown"}
		if err := s.pool.Add(ctx, a, cookies); err != nil {
			return nil, err
		}
		if s.redactor != nil {
			for _, v := range cookies {
				s.redactor.Add(v)
			}
		}
		return a, nil
	case "accounts.update":
		id, _ := args["id"].(string)
		if id == "" {
			return nil, errors.New("account id is required")
		}
		return s.pool.Update(ctx, id, args)
	case "accounts.delete":
		id, _ := args["id"].(string)
		if id == "" {
			return nil, errors.New("account id is required")
		}
		return nil, s.pool.Remove(ctx, id)
	case "accounts.remove", "account.remove":
		id, _ := args["id"].(string)
		if id == "" {
			return nil, errors.New("account id is required")
		}
		return nil, s.pool.Remove(ctx, id)
	case "accounts.probe", "account.probe":
		id, _ := args["id"].(string)
		return s.pool.Probe(ctx, id)
	case "nodes.save":
		return s.saveNode(ctx, args)
	case "nodes.delete":
		return nil, s.deleteManaged(ctx, "node", args)
	case "nodes.test", "nodes.probe":
		id, err := requireID(args)
		if err != nil {
			return nil, err
		}
		return s.testNode(ctx, id)
	case "personas.save":
		return s.savePersona(ctx, args)
	case "personas.delete":
		return nil, s.deleteManaged(ctx, "persona", args)
	case "schedules.save":
		return s.saveSchedule(ctx, args)
	case "schedules.delete":
		return nil, s.deleteManaged(ctx, "schedule", args)
	case "stickers.import":
		return s.importStickers(ctx, args)
	case "stickers.update":
		return s.updateSticker(ctx, args)
	case "stickers.delete":
		return nil, s.deleteManaged(ctx, "sticker", args)
	case "backup.export":
		return s.exportBackup(ctx, args)
	case "backup.import":
		return s.importBackup(ctx, args)
	case "asr.test":
		return s.testASR(ctx, args)
	case "asr.download":
		return s.downloadASR(ctx, args)
	case "sessions.history":
		id, _ := args["id"].(string)
		var item sessionRecord
		if err := s.db.Get(ctx, "session", id, &item); err != nil {
			return nil, err
		}
		return map[string]any{"session": item, "messages": item.Messages}, nil
	case "sessions.reset":
		id, _ := args["id"].(string)
		return nil, s.db.Delete(ctx, "session", id)
	case "sessions.rename":
		id, _ := args["id"].(string)
		var item sessionRecord
		if err := s.db.Get(ctx, "session", id, &item); err != nil {
			return nil, err
		}
		item.Title, _ = args["name"].(string)
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		return item, s.db.Put(ctx, "session", id, item, nil)
	case "sessions.export":
		id, _ := args["id"].(string)
		var item sessionRecord
		if err := s.db.Get(ctx, "session", id, &item); err != nil {
			return nil, err
		}
		b, _ := json.MarshalIndent(item, "", "  ")
		return map[string]any{"filename": id + ".json", "content": string(b), "mime": "application/json"}, nil
	case "chat.stop", "stop":
		id, _ := args["session_id"].(string)
		if id == "" {
			id, _ = args["id"].(string)
		}
		return s.Stop(id), nil
	default:
		return nil, fmt.Errorf("unsupported action: %s", name)
	}
}

func (s *Service) Chat(ctx context.Context, req ChatRequest, emit func(ChatEvent) error) (ChatResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.SessionID == "" {
		req.SessionID = secure.ID()
	}
	if req.Model == "" {
		req.Model = "muse-spark"
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.sessionsMu.Lock()
	if old := s.sessions[req.SessionID]; old != nil {
		old()
	}
	s.sessions[req.SessionID] = cancel
	s.sessionsMu.Unlock()
	defer func() { s.sessionsMu.Lock(); delete(s.sessions, req.SessionID); s.sessionsMu.Unlock(); cancel() }()
	timeout := time.Duration(settingInt(s.Settings(), "limits.request_timeout_seconds", 300)) * time.Second
	runCtx, timer := context.WithTimeout(runCtx, timeout)
	defer timer()
	if err := s.acquire(runCtx); err != nil {
		return ChatResult{SessionID: req.SessionID}, err
	}
	defer s.release()
	var result ChatResult
	result.SessionID = req.SessionID
	var history sessionRecord
	if e := s.db.Get(runCtx, "session", req.SessionID, &history); e == nil {
		if history.Platform != "" && (history.Platform != req.Platform || history.UserID != req.UserID) {
			return result, errors.New("session belongs to another user")
		}
	}
	if history.ID == "" {
		history = sessionRecord{ID: req.SessionID, Platform: req.Platform, UserID: req.UserID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	}
	incoming := append([]hatch.Message(nil), req.Messages...)
	if req.Prompt != "" {
		duplicate := len(incoming) > 0 && (incoming[len(incoming)-1].Role == "" || incoming[len(incoming)-1].Role == "user") && strings.TrimSpace(incoming[len(incoming)-1].Content) == strings.TrimSpace(req.Prompt)
		if !duplicate {
			incoming = append(incoming, hatch.Message{Role: "user", Content: req.Prompt})
		}
	}
	req.Messages = append(append([]hatch.Message(nil), history.Messages...), incoming...)
	history.Messages = append(history.Messages, incoming...)
	history.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if history.Title == "" {
		history.Title = req.Prompt
		if len(history.Title) > 80 {
			history.Title = history.Title[:80]
		}
	}
	if e := s.db.Put(runCtx, "session", history.ID, history, nil); e != nil {
		return result, e
	}
	err := s.pool.WithClient(runCtx, func(c *hatch.Client, _ pool.Account) error {
		hs := make([]hatch.Attachment, 0, len(req.Attachments))
		for _, u := range req.Attachments {
			data, e := base64.StdEncoding.DecodeString(u.DataBase64)
			if e != nil {
				return errors.New("invalid attachment encoding")
			}
			if len(data) > 64<<20 {
				return errors.New("attachment exceeds 64 MiB")
			}
			a, e := c.Upload(runCtx, data, u.MIME, u.Filename)
			if e != nil {
				return e
			}
			hs = append(hs, a)
		}
		hreq := hatch.GenerateRequest{NodeID: secure.ID(), Prompt: req.Prompt, Model: req.Model, Persona: req.PersonaID, Messages: req.Messages, Attachments: hs}
		result.NodeID = hreq.NodeID
		generated, e := c.Generate(runCtx, hreq, func(ev hatch.Event) error {
			ce := ChatEvent{Type: ev.Type, Text: ev.Text, NodeID: result.NodeID, SessionID: req.SessionID}
			if ev.Attachment != nil {
				f, err := s.materialize(runCtx, c, *ev.Attachment)
				if err != nil {
					return err
				}
				result.Attachments = append(result.Attachments, f)
				ce.Attachment = &f
			}
			if ce.Type == "done" {
				result.Text += ""
			}
			if emit != nil {
				if err := emit(ce); err != nil {
					return err
				}
			}
			return nil
		})
		result.Text = generated.Text
		if e != nil {
			return e
		}
		history.Messages = append(history.Messages, hatch.Message{Role: "assistant", Content: result.Text})
		history.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if e := s.db.Put(runCtx, "session", history.ID, history, nil); e != nil {
			return e
		}
		return nil
	})
	return result, err
}
func (s *Service) Stop(sessionID string) bool {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	c, ok := s.sessions[sessionID]
	if ok {
		c()
		delete(s.sessions, sessionID)
	}
	return ok
}
func (s *Service) File(ctx context.Context, id string) (string, File, error) {
	var f fileRecord
	if err := s.db.Get(ctx, "file", id, &f); err != nil {
		return "", File{}, err
	}
	if f.Path == "" {
		return "", File{}, sql.ErrNoRows
	}
	return f.Path, f.File, nil
}
func (s *Service) Log(ctx context.Context, level, message string) error {
	if s.redactor != nil {
		message = s.redactor.Text(message)
	}
	return s.db.Log(ctx, level, message)
}

func (s *Service) materialize(ctx context.Context, c *hatch.Client, a hatch.Attachment) (File, error) {
	data, err := c.Download(ctx, a)
	if err != nil {
		return File{}, err
	}
	id := secure.ID()
	name := a.Filename
	if name == "" {
		name = "download.bin"
	}
	safe := filepath.Base(name)
	path := filepath.Join(s.dataDir, "files", id+"-"+safe)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return File{}, err
	}
	f := File{ID: id, Filename: safe, MIME: a.MIME}
	if err := s.db.Put(ctx, "file", id, fileRecord{File: f, Path: path}, nil); err != nil {
		return File{}, err
	}
	return f, nil
}
func (s *Service) acquire(ctx context.Context) error {
	s.queueMu.Lock()
	qmax := settingInt(s.Settings(), "limits.queue_size", 20)
	if qmax >= 0 && s.queued >= qmax+cap(s.gate) {
		s.queueMu.Unlock()
		return errors.New("request queue is full")
	}
	s.queued++
	s.queueMu.Unlock()
	select {
	case s.gate <- struct{}{}:
		s.queueMu.Lock()
		s.queued--
		s.queueMu.Unlock()
		return nil
	case <-ctx.Done():
		s.queueMu.Lock()
		s.queued--
		s.queueMu.Unlock()
		return ctx.Err()
	}
}
func (s *Service) release()        { <-s.gate }
func (s *Service) queueDepth() int { s.queueMu.Lock(); defer s.queueMu.Unlock(); return s.queued }
func settingInt(m map[string]any, k string, d int) int {
	switch v := m[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return d
}
func normalizeSetting(v any) any {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return n
	}
}
func cloneAnyMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
