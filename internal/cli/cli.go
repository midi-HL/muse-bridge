// Package cli implements a thin client of the same HTTP/action registry as WebUI.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/midi-HL/muse-bridge/internal/config"
	"github.com/midi-HL/muse-bridge/internal/core"
)

type Runner struct {
	In            io.Reader
	Out           io.Writer
	Err           io.Writer
	Env           func(string) string
	Serve         func(context.Context, []string) error
	EphemeralChat func(context.Context, string, string, string, func(string) error) error
	Client        *http.Client
}
type credentials struct {
	AdminCookie string `json:"admin_cookie"`
	CSRF        string `json:"csrf_token"`
	APIKey      string `json:"api_key"`
}
type client struct {
	base string
	cred credentials
	http *http.Client
}

func (r Runner) Run(ctx context.Context, args []string) error {
	if r.In == nil {
		r.In = os.Stdin
	}
	if r.Out == nil {
		r.Out = os.Stdout
	}
	if r.Err == nil {
		r.Err = os.Stderr
	}
	if r.Env == nil {
		r.Env = os.Getenv
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, e := io.WriteString(r.Out, usage)
		return e
	}
	if args[0] == "serve" {
		if r.Serve == nil {
			return errors.New("serve is unavailable")
		}
		return r.Serve(ctx, args[1:])
	}
	root := flag.NewFlagSet("muse-bridge", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	endpoint := root.String("url", envDefault(r.Env, "MUSE_URL", "http://127.0.0.1:8787"), "")
	authFile := root.String("auth-file", r.Env("MUSE_AUTH_FILE"), "")
	if root.Parse(args) != nil {
		return errors.New("invalid global options; see muse-bridge help")
	}
	args = root.Args()
	if len(args) == 0 {
		return errors.New("command required")
	}
	u, e := url.Parse(*endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid service URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return errors.New("remote service connections require HTTPS")
	}
	cred := credentials{}
	if *authFile != "" {
		b, e := privateRead(*authFile, 64<<10)
		if e != nil {
			return e
		}
		if json.Unmarshal(b, &cred) != nil {
			return errors.New("invalid credentials file")
		}
	}
	if v := r.Env("MUSE_ADMIN_COOKIE"); v != "" {
		cred.AdminCookie = v
	}
	if v := r.Env("MUSE_CSRF_TOKEN"); v != "" {
		cred.CSRF = v
	}
	if v := r.Env("MUSE_API_KEY"); v != "" {
		cred.APIKey = v
	}
	h := &http.Client{Timeout: 31 * time.Minute}
	if r.Client != nil {
		*h = *r.Client
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := client{base: strings.TrimRight(*endpoint, "/"), cred: cred, http: h}
	switch args[0] {
	case "status":
		v, e := c.get(ctx, "/admin/api/auth/status")
		if e != nil {
			return e
		}
		delete(v, "csrf_token")
		return r.print(v)
	case "config":
		return r.config(ctx, &c, args[1:])
	case "accounts":
		return r.accounts(ctx, &c, args[1:])
	case "chat":
		return r.chat(ctx, &c, args[1:])
	case "keys":
		return r.keys(ctx, &c, args[1:])
	case "backup":
		return r.backup(ctx, &c, args[1:])
	case "protocol":
		if len(args) != 2 || args[1] != "probe" {
			return errors.New("usage: protocol probe")
		}
		v, e := c.action(ctx, "protocol.probe", nil)
		if e != nil {
			return e
		}
		return r.print(v)
	case "action":
		return r.action(ctx, &c, args[1:])
	default:
		return errors.New("unknown command; see muse-bridge help")
	}
}
func (r Runner) action(ctx context.Context, c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: action NAME [--args JSON | --args-file PATH]")
	}
	name := args[0]
	var raw, file string
	f, err := parse("action", args[1:], func(f *flag.FlagSet) { f.StringVar(&raw, "args", "{}", ""); f.StringVar(&file, "args-file", "", "") })
	if err != nil {
		return err
	}
	if f.NArg() != 0 || (raw != "{}" && file != "") {
		return errors.New("use one of --args JSON or --args-file PATH")
	}
	if file != "" {
		b, e := privateRead(file, 8<<20)
		if e != nil {
			return e
		}
		raw = string(b)
	}
	argsMap := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if e := json.Unmarshal([]byte(raw), &argsMap); e != nil {
			return errors.New("--args must be a JSON object")
		}
	}
	v, e := c.action(ctx, name, argsMap)
	if e != nil {
		return e
	}
	return r.print(v)
}

func envDefault(env func(string) string, key, fallback string) string {
	if v := env(key); v != "" {
		return v
	}
	return fallback
}
func parse(name string, args []string, setup func(*flag.FlagSet)) (*flag.FlagSet, error) {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	setup(f)
	if f.Parse(args) != nil {
		return nil, errors.New("invalid " + name + " options")
	}
	return f, nil
}
func (r Runner) print(v any) error { return json.NewEncoder(r.Out).Encode(redact(v)) }
func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			lower := strings.ToLower(k)
			f, known := config.Find(k)
			if known && f.Secret || lower == "key" || lower == "secret" || lower == "csrf_token" || strings.Contains(lower, "cookie") || strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "passphrase") || lower == "blob" || lower == "otpauth_url" {
				out[k] = "[REDACTED]"
			} else {
				out[k] = redact(v)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = redact(v)
		}
		return out
	default:
		return v
	}
}
func (c *client) request(ctx context.Context, method, path string, body any, api bool) (*http.Response, error) {
	var data io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, errors.New("invalid request")
		}
		data = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, data)
	if e != nil {
		return nil, errors.New("invalid request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", c.base)
	if api {
		if c.cred.APIKey == "" {
			return nil, errors.New("set MUSE_API_KEY or api_key in credentials file")
		}
		req.Header.Set("Authorization", "Bearer "+c.cred.APIKey)
	} else {
		if c.cred.AdminCookie != "" {
			req.AddCookie(&http.Cookie{Name: "muse_admin", Value: c.cred.AdminCookie})
		}
		if method != http.MethodGet && c.cred.CSRF != "" {
			req.Header.Set("X-CSRF-Token", c.cred.CSRF)
		}
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, errors.New("service connection failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		if api && resp.StatusCode == http.StatusTooManyRequests && (path == "/api/chat" || path == "/api/chat/stream") {
			const limit = 64 << 10
			b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
			if err == nil && len(b) <= limit {
				var failure struct {
					Error json.RawMessage `json:"error"`
				}
				if json.Unmarshal(b, &failure) == nil {
					if err := chatLimitError(failure.Error); err != nil {
						return nil, err
					}
				}
			}
		}
		return nil, fmt.Errorf("service returned HTTP %d; check authorization, input or feature support", resp.StatusCode)
	}
	return resp, nil
}
func (c *client) get(ctx context.Context, path string) (map[string]any, error) {
	resp, e := c.request(ctx, "GET", path, nil, false)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	var out map[string]any
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out) != nil {
		return nil, errors.New("invalid service response")
	}
	return out, nil
}
func (c *client) csrf(ctx context.Context) error {
	if c.cred.AdminCookie == "" {
		return errors.New("set MUSE_ADMIN_COOKIE or admin_cookie in credentials file")
	}
	if c.cred.CSRF != "" {
		return nil
	}
	v, e := c.get(ctx, "/admin/api/auth/status")
	if e != nil {
		return e
	}
	c.cred.CSRF, _ = v["csrf_token"].(string)
	if c.cred.CSRF == "" {
		return errors.New("administrator session expired")
	}
	return nil
}
func (c *client) action(ctx context.Context, name string, args map[string]any) (any, error) {
	if e := c.csrf(ctx); e != nil {
		return nil, e
	}
	resp, e := c.request(ctx, "POST", "/admin/api/action", map[string]any{"action": name, "args": args}, false)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	var out struct {
		OK   bool `json:"ok"`
		Data any  `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 96<<20)).Decode(&out) != nil || !out.OK {
		return nil, errors.New("invalid action response")
	}
	return out.Data, nil
}
func (r Runner) config(ctx context.Context, c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: config schema|get|set")
	}
	switch args[0] {
	case "schema":
		v, e := c.get(ctx, "/admin/api/schema")
		if e != nil {
			return e
		}
		return r.print(v)
	case "get":
		v, e := c.get(ctx, "/admin/api/state")
		if e != nil {
			return e
		}
		settings, _ := v["settings"].(map[string]any)
		if len(args) > 1 {
			return r.print(map[string]any{args[1]: settings[args[1]]})
		}
		return r.print(settings)
	case "set":
		if len(args) < 2 {
			return errors.New("usage: config set KEY --value JSON | --value-env ENV")
		}
		key := args[1]
		var value, valueEnv string
		f, e := parse("config set", args[2:], func(f *flag.FlagSet) {
			f.StringVar(&value, "value", "", "")
			f.StringVar(&valueEnv, "value-env", "", "")
		})
		if e != nil {
			return e
		}
		if f.NArg() != 0 {
			return errors.New("unexpected config arguments")
		}
		field, ok := config.Find(key)
		if !ok {
			return errors.New("unknown setting")
		}
		if field.Secret && valueEnv == "" {
			return errors.New("secret settings require --value-env")
		}
		if valueEnv != "" {
			value = r.Env(valueEnv)
			if value == "" {
				return errors.New("value environment variable is empty")
			}
		}
		var v any = value
		if !field.Secret {
			if e := json.Unmarshal([]byte(value), &v); e != nil {
				return errors.New("--value must be valid JSON")
			}
		}
		out, e := c.action(ctx, "settings.update", map[string]any{key: v})
		if e != nil {
			return e
		}
		return r.print(out)
	default:
		return errors.New("unknown config operation")
	}
}
func (r Runner) accounts(ctx context.Context, c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: accounts import|list|probe")
	}
	switch args[0] {
	case "list":
		v, e := c.action(ctx, "accounts.list", nil)
		if e != nil {
			return e
		}
		return r.print(v)
	case "probe":
		if len(args) > 2 {
			return errors.New("usage: accounts probe [ID]")
		}
		id := ""
		if len(args) > 1 {
			id = args[1]
		}
		action := "accounts.probe"
		if id == "" {
			action = "protocol.probe"
		}
		v, e := c.action(ctx, action, map[string]any{"id": id})
		if e != nil {
			return e
		}
		return r.print(v)
	case "import":
		var file, env string
		f, e := parse("accounts import", args[1:], func(f *flag.FlagSet) { f.StringVar(&file, "file", "", ""); f.StringVar(&env, "env", "", "") })
		if e != nil {
			return e
		}
		if f.NArg() != 0 || (file == "") == (env == "") {
			return errors.New("use exactly one of --file PATH or --env NAME")
		}
		var text string
		if file != "" {
			b, e := privateRead(file, 16<<20)
			if e != nil {
				return e
			}
			text = string(b)
		} else {
			text = r.Env(env)
		}
		if text == "" {
			return errors.New("empty account import")
		}
		v, e := c.action(ctx, "accounts.import", map[string]any{"text": text})
		if e != nil {
			return e
		}
		return r.print(v)
	default:
		return errors.New("unknown accounts operation")
	}
}
func (r Runner) keys(ctx context.Context, c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: keys create|revoke")
	}
	if args[0] == "revoke" {
		if len(args) != 2 {
			return errors.New("usage: keys revoke ID")
		}
		v, e := c.action(ctx, "keys.revoke", map[string]any{"id": args[1]})
		if e != nil {
			return e
		}
		return r.print(v)
	}
	if args[0] != "create" {
		return errors.New("unknown keys operation")
	}
	var name, out string
	var rpm, daily int
	f, e := parse("keys create", args[1:], func(f *flag.FlagSet) {
		f.StringVar(&name, "name", "", "")
		f.StringVar(&out, "out", "", "")
		f.IntVar(&rpm, "rpm", 60, "")
		f.IntVar(&daily, "daily", 1000, "")
	})
	if e != nil {
		return e
	}
	if f.NArg() != 0 || name == "" || out == "" {
		return errors.New("usage: keys create --name NAME --out PRIVATE_FILE [--rpm 60 --daily 1000]")
	}
	// Reserve first so an existing file never causes a newly created key to be lost.
	file, e := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return errors.New("cannot create private output file; choose an absent path")
	}
	defer file.Close()
	removeOutput := func() {
		_ = file.Close()
		_ = os.Remove(out)
	}
	v, e := c.action(ctx, "keys.create", map[string]any{"name": name, "rpm": rpm, "daily": daily})
	if e != nil {
		removeOutput()
		return e
	}
	m, ok := v.(map[string]any)
	if !ok {
		removeOutput()
		return errors.New("invalid key creation response")
	}
	key, _ := m["key"].(string)
	if key == "" {
		removeOutput()
		return errors.New("key response was empty")
	}
	if _, e = io.WriteString(file, key+"\n"); e != nil {
		_, _ = c.action(ctx, "keys.revoke", map[string]any{"id": m["id"]})
		removeOutput()
		return errors.New("key write failed; revocation attempted")
	}
	if e = file.Sync(); e != nil {
		_, _ = c.action(ctx, "keys.revoke", map[string]any{"id": m["id"]})
		removeOutput()
		return errors.New("key sync failed; revocation attempted")
	}
	delete(m, "key")
	m["saved"] = true
	return r.print(m)
}
func (r Runner) backup(ctx context.Context, c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: backup export|import --file PATH --passphrase-env NAME")
	}
	var file, passEnv string
	f, e := parse("backup", args[1:], func(f *flag.FlagSet) {
		f.StringVar(&file, "file", "", "")
		f.StringVar(&passEnv, "passphrase-env", "MUSE_BACKUP_PASSPHRASE", "")
	})
	if e != nil {
		return e
	}
	if f.NArg() != 0 || file == "" {
		return errors.New("backup requires --file")
	}
	pass := r.Env(passEnv)
	if len(pass) < 12 || len(pass) > 256 {
		return errors.New("backup passphrase environment must contain 12–256 bytes")
	}
	switch args[0] {
	case "export":
		f, e := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return errors.New("cannot create backup file")
		}
		defer f.Close()
		removeOutput := func() {
			_ = f.Close()
			_ = os.Remove(file)
		}
		v, e := c.action(ctx, "backup.export", map[string]any{"passphrase": pass})
		if e != nil {
			removeOutput()
			return e
		}
		m, ok := v.(map[string]any)
		if !ok {
			removeOutput()
			return errors.New("invalid backup response")
		}
		blob, _ := m["content"].(string)
		if blob == "" {
			blob, _ = m["blob"].(string)
		}
		if !strings.HasPrefix(blob, "MB1.") {
			removeOutput()
			return errors.New("service did not return an encrypted backup")
		}
		if _, e = io.WriteString(f, blob); e != nil {
			removeOutput()
			return errors.New("backup write failed")
		}
		if f.Sync() != nil {
			removeOutput()
			return errors.New("backup sync failed")
		}
		return r.print(map[string]any{"saved": true})
	case "import":
		b, e := privateRead(file, 64<<20)
		if e != nil {
			return e
		}
		v, e := c.action(ctx, "backup.import", map[string]any{"passphrase": pass, "blob": strings.TrimSpace(string(b))})
		if e != nil {
			return e
		}
		return r.print(v)
	default:
		return errors.New("unknown backup operation")
	}
}
func (r Runner) chat(ctx context.Context, c *client, args []string) error {
	var prompt, model, session, cookie, persona string
	var stream bool
	f, e := parse("chat", args, func(f *flag.FlagSet) {
		f.StringVar(&prompt, "prompt", "", "")
		f.StringVar(&model, "model", "muse-spark", "")
		f.StringVar(&session, "session", "", "")
		f.StringVar(&persona, "persona", "", "")
		f.StringVar(&cookie, "cookie", "", "")
		f.BoolVar(&stream, "stream", true, "")
	})
	if e != nil {
		return e
	}
	if f.NArg() > 0 {
		prompt = strings.Join(f.Args(), " ")
	}
	if prompt == "" {
		b, e := io.ReadAll(io.LimitReader(r.In, 256<<10))
		if e != nil {
			return errors.New("could not read prompt")
		}
		prompt = strings.TrimSpace(string(b))
	}
	if prompt == "" {
		return errors.New("prompt required")
	}
	if cookie != "" {
		if persona != "" {
			return errors.New("saved personas require server chat, without --cookie")
		}
		if r.EphemeralChat == nil {
			return errors.New("ephemeral cookie chat is unsupported")
		}
		var value string
		if strings.HasPrefix(cookie, "env:") {
			value = r.Env(strings.TrimPrefix(cookie, "env:"))
		} else if strings.HasPrefix(cookie, "file:") {
			b, e := privateRead(strings.TrimPrefix(cookie, "file:"), 64<<10)
			if e != nil {
				return e
			}
			value = string(b)
		} else {
			return errors.New("--cookie accepts only env:NAME or file:PATH; never pass raw cookies")
		}
		if value == "" {
			return errors.New("cookie source is empty")
		}
		e = r.EphemeralChat(ctx, value, prompt, model, func(delta string) error { _, e := io.WriteString(r.Out, delta); return e })
		if e != nil {
			return errors.New("cookie chat failed; verify credentials and protocol availability")
		}
		_, e = fmt.Fprintln(r.Out)
		return e
	}
	body := core.ChatRequest{Prompt: prompt, Model: model, SessionID: session, PersonaID: persona}
	path := "/api/chat"
	if stream {
		path += "/stream"
	}
	resp, e := c.request(ctx, "POST", path, body, true)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if !stream {
		var result core.ChatResult
		if json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&result) != nil {
			return errors.New("invalid chat response")
		}
		_, e = fmt.Fprintln(r.Out, result.Text)
		return e
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	done := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			core.ChatEvent
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			return errors.New("invalid stream event")
		}
		switch event.Type {
		case "delta", "text":
			if _, e = io.WriteString(r.Out, event.Text); e != nil {
				return e
			}
		case "error":
			if err := chatLimitError(event.Error); err != nil {
				return err
			}
			return errors.New("generation failed; check service account availability")
		case "done":
			done = true
		}
	}
	if scanner.Err() != nil || !done {
		return errors.New("chat stream ended before completion")
	}
	_, e = fmt.Fprintln(r.Out)
	return e
}
func chatLimitError(raw json.RawMessage) error {
	// Only recognized codes select a message. Server-supplied text stays private.
	var detail struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &detail) == nil {
		switch detail.Code {
		case "daily_limit":
			return errors.New("daily chat request limit reached")
		case "queue_full":
			return errors.New("request queue is full; retry later")
		}
	}
	return nil
}

func privateRead(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("cannot open private input file")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errors.New("invalid or oversized private input file")
	}
	if st.Mode().Perm()&0077 != 0 && os.PathSeparator != '\\' {
		return nil, errors.New("private input file must not be accessible by group or others (chmod 600)")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, errors.New("could not read private input file")
	}
	return b, nil
}

const usage = `Muse Bridge
  muse-bridge serve [--addr 127.0.0.1:8787] [--data-dir data] [--config settings.yaml]
  muse-bridge [--url URL] [--auth-file PRIVATE_JSON] status
  muse-bridge [global options] config schema|get [KEY]
  muse-bridge [global options] config set KEY --value JSON | --value-env NAME
  muse-bridge [global options] accounts import --file PRIVATE_FILE | --env NAME
  muse-bridge [global options] accounts list|probe [ID]
  muse-bridge [global options] chat [--prompt TEXT] [--model muse-spark] [--persona ID|_none] [--stream=true]
  muse-bridge chat --cookie env:NAME|file:PRIVATE_FILE [--prompt TEXT]
  muse-bridge [global options] keys create --name NAME --out PRIVATE_FILE
  muse-bridge [global options] keys revoke ID
  muse-bridge [global options] backup export|import --file FILE [--passphrase-env NAME]
  muse-bridge [global options] protocol probe
  muse-bridge [global options] action NAME [--args JSON | --args-file PATH]

Credentials: MUSE_ADMIN_COOKIE, MUSE_CSRF_TOKEN, MUSE_API_KEY, MUSE_AUTH_FILE.
Secrets never belong in command-line arguments. Global options precede the command.
Remote HTTP is refused; use HTTPS. Key creation saves the key once to a new private file.
`
