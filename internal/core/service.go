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
	life               context.Context
	cancel             context.CancelFunc
	db                 *store.Store
	vault              *secure.Vault
	redactor           *secure.Redactor
	dataDir            string
	fileRoot           *os.Root
	settingsMu         sync.RWMutex
	settings           map[string]any
	settingsHook       func(map[string]any) error
	pool               *pool.Pool
	generate           func(context.Context, *hatch.Client, hatch.GenerateRequest, func(hatch.Event) error) (hatch.GenerateResult, error)
	media              *media.Processor
	gate               chan struct{}
	queueMu            sync.Mutex
	queued             int
	sessionsMu         sync.Mutex
	sessions           map[string]context.CancelFunc
	telegramSessionMu  sync.Mutex
	telegramCommandMu  sync.Mutex
	telegramCommands   map[string]chan struct{}
	telegramDeliveries map[string]chan struct{}
	scheduleMu         sync.Mutex
	scheduleCallback   ScheduleCallback
	scheduleRuns       map[string]bool
	schedulerWG        sync.WaitGroup
	executionWG        sync.WaitGroup
	closeOnce          sync.Once
}

type fileRecord struct {
	File
	Path     string `json:"path"`
	Platform string `json:"platform,omitempty"`
	UserID   string `json:"user_id,omitempty"`
}

type sessionRecord struct {
	ID              string          `json:"id"`
	Platform        string          `json:"platform"`
	UserID          string          `json:"user_id"`
	Title           string          `json:"title"`
	Messages        []hatch.Message `json:"messages"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
	PersonaID       string          `json:"persona_id"`
	PersonaSelected bool            `json:"persona_selected"`
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
	fileRoot, err := os.OpenRoot(filepath.Join(dataDir, "files"))
	if err != nil {
		cancel()
		_ = p.Close()
		return nil, errors.New("cannot open generated file directory")
	}
	s := &Service{life: life, cancel: cancel, db: db, vault: vault, redactor: redactor, dataDir: dataDir, fileRoot: fileRoot, settings: settings, pool: p, media: mp, gate: make(chan struct{}, concurrency), sessions: map[string]context.CancelFunc{}, scheduleRuns: map[string]bool{}}
	s.generate = func(ctx context.Context, client *hatch.Client, req hatch.GenerateRequest, emit func(hatch.Event) error) (hatch.GenerateResult, error) {
		return client.Generate(ctx, req, emit)
	}
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
		_ = s.fileRoot.Close()
	})
	return nil
}

// SetGenerateForTest replaces the generation adapter for deterministic package-level
// integration tests and returns a function restoring the previous adapter.
func (s *Service) SetGenerateForTest(fn func(context.Context, *hatch.Client, hatch.GenerateRequest, func(hatch.Event) error) (hatch.GenerateResult, error)) func() {
	previous := s.generate
	s.generate = fn
	return func() { s.generate = previous }
}

func (s *Service) Settings() map[string]any {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return publicSettings(s.settings)
}

// RuntimeSettings is for trusted in-process adapters only (Telegram/webhook,
// media, schedulers). It must never be returned from an HTTP or CLI response.
func (s *Service) RuntimeSettings() map[string]any {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return cloneAnyMap(s.settings)
}
func (s *Service) Schema() map[string]any {
	return map[string]any{"settings": append([]config.Field(nil), config.Fields...), "operations": []string{
		"state", "schema", "settings", "settings.update", "log",
		"accounts", "accounts.list", "accounts.import", "accounts.update", "accounts.delete", "accounts.probe",
		"protocol.probe", "protocol.tasks", "protocol.threads",
		"nodes.save", "nodes.delete", "nodes.test", "nodes.probe",
		"personas.save", "personas.delete",
		"schedules.save", "schedules.delete",
		"stickers.import", "stickers.update", "stickers.delete",
		"backup.export", "backup.import",
		"asr.test", "asr.download",
		"sessions.history", "sessions.reset", "sessions.rename", "sessions.export", "sessions.persona", "chat.stop",
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
			sessions = append(sessions, map[string]any{"id": item.ID, "platform": item.Platform, "user_id": item.UserID, "title": item.Title, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt, "persona_id": item.PersonaID, "persona_selected": item.PersonaSelected})
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
				fields := cloneAnyMap(item.Fields)
				fields["id"] = id
				a, err := s.pool.Import(ctx, id, fields, item.Cookies)
				if err != nil {
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
		fields := map[string]any{"id": id}
		for _, key := range []string{"label", "node_id", "pool", "note", "enabled"} {
			if value, exists := args[key]; exists {
				fields[key] = value
			}
		}
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
		a, err := s.pool.Import(ctx, id, fields, cookies)
		if err != nil {
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
	case "protocol.probe":
		return s.protocolProbe(ctx, args)
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
	case "sessions.persona":
		id, _ := args["id"].(string)
		personaID, ok := args["persona_id"].(string)
		if !ok {
			return nil, errors.New("persona_id is required")
		}
		return s.SetSessionPersona(ctx, id, "", "", personaID)
	case "sessions.history":
		id, _ := args["id"].(string)
		var item sessionRecord
		if err := s.db.Get(ctx, "session", id, &item); err != nil {
			return nil, err
		}
		return map[string]any{"session": item, "messages": item.Messages}, nil
	case "sessions.reset":
		id, _ := args["id"].(string)
		s.sessionsMu.Lock()
		defer s.sessionsMu.Unlock()
		s.telegramCommandMu.Lock()
		defer s.telegramCommandMu.Unlock()
		if deliveryPending, err := s.telegramCommandDeliveryPendingLocked(id); err != nil {
			return nil, err
		} else if deliveryPending {
			return nil, errors.New("Telegram command delivery is in progress")
		}
		if s.sessions[id] != nil {
			return nil, errors.New("session is generating")
		}
		if err := s.invalidateTelegramRetries(ctx, id); err != nil {
			return nil, err
		}
		if err := s.invalidateTelegramCommands(ctx, id); err != nil {
			return nil, err
		}
		err := s.db.Delete(ctx, "session", id)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		return nil, err
	case "sessions.rename":
		id, _ := args["id"].(string)
		s.sessionsMu.Lock()
		defer s.sessionsMu.Unlock()
		if s.sessions[id] != nil {
			return nil, errors.New("session is generating")
		}
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
	stopLifecycle := context.AfterFunc(s.life, cancel)
	defer stopLifecycle()
	s.sessionsMu.Lock()
	if s.sessions[req.SessionID] != nil {
		s.sessionsMu.Unlock()
		cancel()
		return ChatResult{SessionID: req.SessionID}, errors.New("session is already generating")
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
	var retryState telegramRetryState
	if req.Retry {
		if req.Platform != "telegram" || req.RetryUserID == "" || req.RetryAttemptID == "" {
			return result, errors.New("retry is supported only for Telegram requests")
		}
		var loadErr error
		retryState, loadErr = s.telegramRetryRecord(runCtx, req.SessionID, req.RetryUserID)
		if loadErr != nil || retryState.Owner != req.RetryUserID || retryState.Prompt == "" || retryState.AttemptID != req.RetryAttemptID {
			return result, errors.New("no previous message to retry")
		}
		chatID := telegramChatIDFromSession(req.SessionID)
		var pointer telegramSessionRecord
		if chatID == 0 || s.db.Get(runCtx, "telegram_session", telegramSessionKey(chatID), &pointer) != nil || pointer.SessionID != req.SessionID {
			return result, errors.New("no previous message to retry")
		}
	}
	var history sessionRecord
	if e := s.db.Get(runCtx, "session", req.SessionID, &history); e == nil {
		if history.Platform != req.Platform || history.UserID != req.UserID {
			return result, errors.New("session belongs to another user")
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return result, e
	} else if req.Retry {
		history = sessionRecord{ID: req.SessionID, Platform: req.Platform, UserID: req.UserID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	}
	if history.ID == "" {
		history = sessionRecord{ID: req.SessionID, Platform: req.Platform, UserID: req.UserID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	}
	if req.Platform == "telegram" && !req.Retry && strings.TrimSpace(req.Prompt) != "" {
		owner := req.RetryUserID
		if owner == "" {
			owner = req.UserID
		}
		if _, err := s.beginTelegramAttempt(runCtx, req.SessionID, owner, req.Prompt); err != nil {
			return result, err
		}
	}
	personaID, persona, err := s.resolvePersona(runCtx, req.PersonaID, history)
	if err != nil {
		return result, err
	}
	history.PersonaID, history.PersonaSelected = personaID, true
	incoming := append([]hatch.Message(nil), req.Messages...)
	if req.Retry {
		if len(incoming) != 0 || req.Prompt != "" || len(req.Attachments) != 0 {
			return result, errors.New("retry request must not contain new input")
		}
		req.Prompt = retryState.Prompt
		incoming = []hatch.Message{{Role: "user", Content: req.Prompt}}
	} else if req.Prompt != "" {
		duplicate := len(incoming) > 0 && (incoming[len(incoming)-1].Role == "" || incoming[len(incoming)-1].Role == "user") && strings.TrimSpace(incoming[len(incoming)-1].Content) == strings.TrimSpace(req.Prompt)
		if !duplicate {
			incoming = append(incoming, hatch.Message{Role: "user", Content: req.Prompt})
		}
	}
	req.Messages = append(append([]hatch.Message(nil), history.Messages...), incoming...)
	// History is committed only after generation succeeds. An explicit retry is
	// a new turn; it never deletes another sender's earlier successful answer.
	history.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if history.Title == "" {
		history.Title = req.Prompt
		if len(history.Title) > 80 {
			history.Title = history.Title[:80]
		}
	}
	split, _ := s.Settings()["pool.split"].(bool)
	err = s.pool.WithClientFor(runCtx, req.Platform, split, func(c *hatch.Client, _ pool.Account) error {
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
		hreq := hatch.GenerateRequest{NodeID: secure.ID(), Prompt: req.Prompt, Model: req.Model, Persona: persona, Messages: req.Messages, Attachments: hs}
		result.NodeID = hreq.NodeID
		generated, e := s.generate(runCtx, c, hreq, func(ev hatch.Event) error {
			ce := ChatEvent{Type: ev.Type, Text: ev.Text, NodeID: result.NodeID, SessionID: req.SessionID}
			if ev.Attachment != nil {
				f, err := s.materialize(runCtx, c, *ev.Attachment, req.Platform, req.UserID)
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
		history.Messages = append(history.Messages, incoming...)
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
		// Keep the lease until Chat unwinds so a replacement request cannot
		// race the cancelled request's history write or deferred cleanup.
		c()
	}
	return ok
}
func (s *Service) Log(ctx context.Context, level, message string) error {
	if s.redactor != nil {
		message = s.redactor.Text(message)
	}
	return s.db.Log(ctx, level, message)
}

func (s *Service) materialize(ctx context.Context, c *hatch.Client, a hatch.Attachment, platform, userID string) (File, error) {
	data, err := c.Download(ctx, a)
	if err != nil {
		return File{}, err
	}
	return s.storeGeneratedFile(ctx, a.Filename, a.MIME, data, platform, userID)
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
