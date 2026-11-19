package core

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/media"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

const maxBackupBytes = 64 << 20

var managedID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type nodeRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	MaskedURL string `json:"masked_url,omitempty"`
	// URLMasked is retained for reading older records and API clients.
	URLMasked string `json:"url_masked,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	TestedAt  string `json:"tested_at"`
	Error     string `json:"error"`
	Status    string `json:"status"`
}

type personaRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Prompt  string `json:"prompt"`
	Enabled bool   `json:"enabled"`
}

type scheduleRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Platform string `json:"platform"`
	Target   string `json:"target"`
	Prompt   string `json:"prompt"`
	Cron     string `json:"cron"`
	Timezone string `json:"timezone"`
}

type stickerRecord struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	URL         string `json:"url,omitempty"`
	Pack        string `json:"pack,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	FileID      string `json:"file_id,omitempty"`
	MIME        string `json:"mime,omitempty"`
	FilePath    string `json:"file_path,omitempty"`
	Description string `json:"description,omitempty"`
	Size        int64  `json:"size,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
}

func requireID(args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" || !managedID.MatchString(id) {
		return "", errors.New("id is required and must be a safe identifier")
	}
	return id, nil
}

func textArg(args map[string]any, key string, required bool, max int) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		if required {
			return "", fmt.Errorf("%s is required", key)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be text", key)
	}
	if required && strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	if len(s) > max || strings.ContainsAny(s, "\x00\r") {
		return "", fmt.Errorf("%s is invalid or too long", key)
	}
	return s, nil
}

func proxyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" {
		return errors.New("proxy URL is invalid")
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("proxy URL scheme is unsupported")
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(raw, "\x00\r\n") {
		return errors.New("proxy URL is invalid")
	}
	portText := u.Port()
	if (scheme == "socks5" || scheme == "socks5h") && portText == "" {
		return errors.New("proxy URL is invalid")
	}
	if portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("proxy URL is invalid")
		}
	}
	return nil
}

func maskProxyURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "••••••••"
	}
	host := u.Hostname()
	if host == "" {
		return u.Scheme + "://••••••••"
	}
	port := u.Port()
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://••••••••@" + host
}

func (s *Service) saveNode(ctx context.Context, args map[string]any) (any, error) {
	id, _ := args["id"].(string)
	if id == "" {
		id = secure.ID()
	}
	if !managedID.MatchString(id) {
		return nil, errors.New("id is invalid")
	}
	name, err := textArg(args, "name", true, 256)
	if err != nil {
		return nil, err
	}
	raw, hasURL := args["url"]
	urlText := ""
	if hasURL && raw != nil {
		urlText, err = textArg(args, "url", true, 4096)
		if err != nil {
			return nil, err
		}
	}
	if urlText == "" {
		var secret map[string]string
		if e := s.db.Secret(ctx, "node", id, &secret); e != nil {
			return nil, errors.New("url is required for a new node")
		}
		urlText = secret["url"]
	}
	if err := proxyURL(urlText); err != nil {
		return nil, err
	}
	enabled := true
	if v, ok := args["enabled"].(bool); ok {
		enabled = v
	}
	item := nodeRecord{ID: id, Name: name, Enabled: enabled, MaskedURL: maskProxyURL(urlText)}
	if err := s.db.Put(ctx, "node", id, item, map[string]string{"url": urlText}); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) listNodes(ctx context.Context) ([]any, error) {
	rows, err := s.db.List(ctx, "node")
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		var item nodeRecord
		if json.Unmarshal(row.Data, &item) != nil {
			continue
		}
		if item.ID == "" {
			item.ID = row.ID
		}
		if item.MaskedURL == "" {
			if item.URLMasked != "" {
				item.MaskedURL = item.URLMasked
				item.URLMasked = ""
			} else {
				var secret map[string]string
				if s.db.Secret(ctx, "node", item.ID, &secret) == nil {
					item.MaskedURL = maskProxyURL(secret["url"])
				}
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) listManaged(ctx context.Context, kind string) ([]any, error) {
	rows, err := s.db.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		var item map[string]any
		if json.Unmarshal(row.Data, &item) != nil {
			continue
		}
		if _, ok := item["id"]; !ok {
			item["id"] = row.ID
		}
		delete(item, "url")
		out = append(out, item)
	}
	return out, nil
}

func (s *Service) savePersona(ctx context.Context, args map[string]any) (any, error) {
	id, _ := args["id"].(string)
	if id == "" {
		id = secure.ID()
	}
	if !managedID.MatchString(id) {
		return nil, errors.New("id is invalid")
	}
	name, err := textArg(args, "name", true, 256)
	if err != nil {
		return nil, err
	}
	prompt, err := textArg(args, "prompt", true, 32768)
	if err != nil {
		return nil, err
	}
	item := personaRecord{ID: id, Name: name, Prompt: prompt, Enabled: true}
	if v, ok := args["enabled"].(bool); ok {
		item.Enabled = v
	}
	if err := s.db.Put(ctx, "persona", id, item, nil); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) saveSchedule(ctx context.Context, args map[string]any) (any, error) {
	id, _ := args["id"].(string)
	if id == "" {
		id = secure.ID()
	}
	if !managedID.MatchString(id) {
		return nil, errors.New("id is invalid")
	}
	name, err := textArg(args, "name", true, 256)
	if err != nil {
		return nil, err
	}
	platform, err := textArg(args, "platform", true, 64)
	if err != nil {
		return nil, err
	}
	target, err := textArg(args, "target", true, 1024)
	if err != nil {
		return nil, err
	}
	prompt, err := textArg(args, "prompt", true, 32768)
	if err != nil {
		return nil, err
	}
	cron, err := textArg(args, "cron", true, 256)
	if err != nil {
		return nil, err
	}
	if len(strings.Fields(cron)) != 5 || !validCron(cron) {
		return nil, errors.New("cron must contain five valid fields")
	}
	tz, err := textArg(args, "timezone", true, 128)
	if err != nil {
		return nil, err
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return nil, errors.New("unknown schedule time zone")
	}
	item := scheduleRecord{ID: id, Name: name, Platform: platform, Target: target, Prompt: prompt, Cron: cron, Timezone: tz, Enabled: true}
	if v, ok := args["enabled"].(bool); ok {
		item.Enabled = v
	}
	if err := s.db.Put(ctx, "schedule", id, item, nil); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) importStickers(ctx context.Context, args map[string]any) (any, error) {
	rawURL, err := textArg(args, "url", true, 4096)
	if err != nil {
		return nil, err
	}
	s.settingsMu.RLock()
	token, _ := s.settings["telegram.token"].(string)
	s.settingsMu.RUnlock()
	if strings.TrimSpace(token) == "" {
		return nil, telegram.ErrNotConfigured
	}
	report, err := telegram.ImportStickerSetContext(ctx, token, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for _, asset := range report.Assets {
		id := asset.ID
		if id == "" {
			id = secure.ID()
		}
		item := stickerRecord{ID: id, Name: report.Pack, URL: rawURL, Pack: asset.Pack, Emoji: asset.Emoji, FileID: asset.FileID, MIME: asset.MIME, FilePath: asset.FilePath}
		if err := s.db.Put(ctx, "sticker", id, item, nil); err != nil {
			report.Failed = append(report.Failed, telegram.StickerImportFailure{ID: id, Error: "storage failure"})
			report.Imported--
		}
	}
	// Return a stable, non-secret summary suitable for API clients.
	return map[string]any{"pack": report.Pack, "total": report.Total, "imported": report.Imported, "failed": report.Failed}, nil
}

func (s *Service) updateSticker(ctx context.Context, args map[string]any) (any, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	description, err := textArg(args, "description", true, 32768)
	if err != nil {
		return nil, err
	}
	var item stickerRecord
	if err := s.db.Get(ctx, "sticker", id, &item); err != nil {
		return nil, err
	}
	item.Description = description
	if err := s.db.Put(ctx, "sticker", id, item, nil); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) deleteManaged(ctx context.Context, kind string, args map[string]any) error {
	id, err := requireID(args)
	if err != nil {
		return err
	}
	return s.db.Delete(ctx, kind, id)
}

func (s *Service) exportBackup(ctx context.Context, args map[string]any) (any, error) {
	pass, err := textArg(args, "passphrase", true, 256)
	if err != nil {
		return nil, err
	}
	blob, err := s.db.Export(ctx)
	if err != nil {
		return nil, err
	}
	sealed, err := secure.BackupEncrypt(pass, blob)
	if err != nil {
		return nil, err
	}
	return map[string]any{"blob": sealed, "content": sealed, "mime": "application/octet-stream", "filename": "muse-backup.enc"}, nil
}

type backupEnvelope struct {
	Version int            `json:"version"`
	Records []backupRecord `json:"records"`
}

type backupRecord struct {
	Kind   string          `json:"kind"`
	ID     string          `json:"id"`
	Data   json.RawMessage `json:"data"`
	Secret json.RawMessage `json:"secret"`
}

var backupKinds = map[string]bool{"account": true, "session": true, "settings": true, "node": true, "persona": true, "sticker": true, "schedule": true, "file": true, "telegram_session": true, "telegram_retry": true, "telegram_command": true, "telegram_chat": true}

type backupSnapshot struct {
	backupRecord
	Exists bool
}

func (s *Service) restoreBackupSnapshots(ctx context.Context, snapshots []backupSnapshot) error {
	var firstErr error
	// Restore in reverse order so a failed import is unwound like a stack. The
	// delete-before-put is required because Store.Put preserves an old secret
	// when its secret argument is nil.
	for i := len(snapshots) - 1; i >= 0; i-- {
		snap := snapshots[i]
		if err := s.db.Delete(ctx, snap.Kind, snap.ID); err != nil && !errors.Is(err, sql.ErrNoRows) && firstErr == nil {
			firstErr = err
		}
		if !snap.Exists {
			continue
		}
		var data any
		if err := json.Unmarshal(snap.Data, &data); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var secret any
		if len(snap.Secret) > 0 && string(snap.Secret) != "null" {
			if err := json.Unmarshal(snap.Secret, &secret); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		if err := s.db.Put(ctx, snap.Kind, snap.ID, data, secret); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) importBackup(ctx context.Context, args map[string]any) (any, error) {
	pass, err := textArg(args, "passphrase", true, 256)
	if err != nil {
		return nil, err
	}
	blob, _ := args["blob"].(string)
	if blob == "" {
		blob, _ = args["content"].(string)
	}
	if blob == "" || len(blob) > maxBackupBytes*2 {
		return nil, errors.New("encrypted backup is required or too large")
	}
	plain, err := secure.BackupDecrypt(pass, blob)
	if err != nil {
		return nil, err
	}
	if len(plain) > maxBackupBytes {
		return nil, errors.New("backup exceeds size limit")
	}
	var env backupEnvelope
	if err := json.Unmarshal(plain, &env); err != nil || env.Version != 1 {
		return nil, errors.New("invalid backup records")
	}

	seen := make(map[string]bool, len(env.Records))
	for index, row := range env.Records {
		// Store.Export already excludes auth/admin_session/api_key. Keep the
		// allowlist here too so a hand-crafted backup cannot reintroduce them.
		validID := managedID.MatchString(row.ID)
		if row.Kind == "file" {
			validID = validFileID(row.ID)
		}
		if !backupKinds[row.Kind] || !validID || len(row.Data) == 0 || !json.Valid(row.Data) {
			return nil, errors.New("backup contains an invalid or forbidden record")
		}
		if err := validateTelegramBackupRecord(row); err != nil {
			return nil, errors.New("backup contains an invalid or forbidden Telegram record")
		}
		if row.Kind == "file" {
			var rec fileRecord
			if json.Unmarshal(row.Data, &rec) != nil || !validFileID(row.ID) || (rec.ID != "" && rec.ID != row.ID) {
				return nil, errors.New("backup contains an unsafe file record")
			}
			name, err := s.importedFileName(rec, row.ID)
			if err != nil {
				return nil, errors.New("backup contains an unsafe file record")
			}
			rec.ID, rec.Path = row.ID, name
			// Persist only the target-root-relative name, not the original host path.
			normalized, err := json.Marshal(rec)
			if err != nil {
				return nil, errors.New("backup contains an unsafe file record")
			}
			env.Records[index].Data = normalized
			// A metadata-only backup need not include bytes. Missing files stay
			// unavailable, but an existing symlink or special file must be rejected.
			if _, err = s.fileRoot.Lstat(name); err == nil {
				file, openErr := s.openRecordFile(rec)
				if openErr != nil {
					return nil, errors.New("backup contains an unsafe file record")
				}
				_ = file.Close()
			} else if !os.IsNotExist(err) {
				return nil, errors.New("backup contains an unsafe file record")
			}
		}
		key := row.Kind + ":" + row.ID
		if seen[key] {
			return nil, errors.New("backup contains duplicate records")
		}
		seen[key] = true
		if len(row.Secret) > 0 && !json.Valid(row.Secret) {
			return nil, errors.New("backup contains invalid secret data")
		}
	}

	// Snapshot every destination before mutating it. This makes imports
	// overwrite existing records while retaining an exact rollback image.
	snapshots := make([]backupSnapshot, 0, len(env.Records))
	for _, row := range env.Records {
		snap := backupSnapshot{backupRecord: backupRecord{Kind: row.Kind, ID: row.ID}}
		var data json.RawMessage
		err := s.db.Get(ctx, row.Kind, row.ID, &data)
		switch {
		case err == nil:
			snap.Exists = true
			snap.Data = append([]byte(nil), data...)
			var secret json.RawMessage
			secretErr := s.db.Secret(ctx, row.Kind, row.ID, &secret)
			if secretErr == nil {
				snap.Secret = append([]byte(nil), secret...)
			} else if !errors.Is(secretErr, sql.ErrNoRows) {
				return nil, secretErr
			}
		case errors.Is(err, sql.ErrNoRows):
			// New destination; snapshot only records its absence.
		default:
			return nil, err
		}
		snapshots = append(snapshots, snap)
	}

	rollback := func(cause error) (any, error) {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if restoreErr := s.restoreBackupSnapshots(rollbackCtx, snapshots); restoreErr != nil {
			return nil, fmt.Errorf("%w (backup rollback failed: %v)", cause, restoreErr)
		}
		return nil, cause
	}

	for _, row := range env.Records {
		var data any
		if err := json.Unmarshal(row.Data, &data); err != nil {
			return rollback(errors.New("invalid backup data"))
		}
		var secret any
		if len(row.Secret) > 0 && string(row.Secret) != "null" {
			if err := json.Unmarshal(row.Secret, &secret); err != nil {
				return rollback(errors.New("invalid backup secret"))
			}
		}
		// Delete first because Store.Put intentionally preserves an old secret
		// when called with a nil secret on an existing record.
		if err := s.db.Delete(ctx, row.Kind, row.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return rollback(err)
		}
		if err := s.db.Put(ctx, row.Kind, row.ID, data, secret); err != nil {
			return rollback(err)
		}
	}
	return map[string]any{"count": len(env.Records)}, nil
}

func (s *Service) testASR(ctx context.Context, args map[string]any) (any, error) {
	encoded, _ := args["data_base64"].(string)
	if encoded == "" {
		return nil, errors.New("data_base64 is required")
	}
	if len(encoded) > 56<<20 {
		return nil, media.ErrTooLarge
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("invalid audio encoding")
	}
	name, _ := args["filename"].(string)
	if name == "" {
		name = "audio.bin"
	}
	tr, err := s.media.Transcribe(ctx, media.Attachment{Kind: "audio", Name: filepath.Base(name), Data: data})
	if err != nil {
		return nil, err
	}
	return tr, nil
}

func (s *Service) downloadASR(ctx context.Context, args map[string]any) (any, error) {
	raw, err := textArg(args, "url", true, 4096)
	if err != nil {
		return nil, err
	}
	expected, err := textArg(args, "sha256", false, 64)
	if err != nil {
		return nil, err
	}
	if expected != "" && !sha256Hex.MatchString(expected) {
		return nil, errors.New("sha256 must contain 64 hexadecimal digits")
	}
	name, err := textArg(args, "filename", false, 1024)
	if err != nil {
		return nil, err
	}
	if name == "" {
		u, _ := url.Parse(raw)
		name = filepath.Base(u.Path)
	}
	data, err := media.SafeDownload(ctx, raw, 128<<20)
	if err != nil {
		return nil, err
	}
	return s.storeDownloadedModel(name, data, expected)
}
