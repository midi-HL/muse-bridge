package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Processor struct {
	cfg     map[string]any
	tempDir string
	client  *http.Client
}

func New(cfg map[string]any, tempDir string) (*Processor, error) {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		return nil, err
	}
	return &Processor{cfg: cfg, tempDir: tempDir, client: &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (p *Processor) Prepare(ctx context.Context, a Attachment) (Prepared, error) {
	if len(a.Data) > 40<<20 {
		return Prepared{}, ErrTooLarge
	}
	switch a.Kind {
	case "image", "sticker":
		return Prepared{Text: fmt.Sprintf("[image:%s]", safeName(a.Name)), Attachments: []Attachment{a}}, nil
	case "video":
		return p.prepareVideo(ctx, a)
	case "voice", "audio":
		tr, e := p.Transcribe(ctx, a)
		if e != nil {
			return Prepared{}, e
		}
		return Prepared{Text: "[语音转写] " + tr.Text}, nil
	default:
		return Prepared{Text: fmt.Sprintf("[file:%s]", safeName(a.Name)), Attachments: []Attachment{a}}, nil
	}
}
func (p *Processor) prepareVideo(ctx context.Context, a Attachment) (Prepared, error) {
	mode, _ := p.cfg["media.video_mode"].(string)
	if mode == "disabled" {
		return Prepared{}, ErrUnsupported
	}
	if mode == "vision" {
		d, e := p.Describe(ctx, []Attachment{a}, "Describe this video briefly.")
		if e != nil {
			return Prepared{}, e
		}
		return Prepared{Text: "[视频描述] " + d}, nil
	}
	return Prepared{Text: fmt.Sprintf("[video:%s]", safeName(a.Name)), Attachments: []Attachment{a}}, nil
}
func (p *Processor) Transcribe(ctx context.Context, a Attachment) (Transcript, error) {
	start := time.Now()
	if len(a.Data) > 40<<20 {
		return Transcript{}, ErrTooLarge
	}
	provider, _ := p.cfg["asr.provider"].(string)
	if provider == "disabled" || provider == "" {
		return Transcript{}, ErrNotConfigured
	}
	tmp, e := os.CreateTemp(p.tempDir, "audio-*")
	if e != nil {
		return Transcript{}, e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, e = tmp.Write(a.Data); e != nil {
		tmp.Close()
		return Transcript{}, e
	}
	tmp.Close()
	if provider == "local" {
		exe, _ := p.cfg["asr.executable"].(string)
		model, _ := p.cfg["asr.model_dir"].(string)
		if exe == "" || model == "" {
			return Transcript{}, ErrNotConfigured
		}
		argsRaw, _ := p.cfg["asr.arguments"].(string)
		var args []string
		if json.Unmarshal([]byte(argsRaw), &args) != nil {
			return Transcript{}, errors.New("media: invalid ASR argument list")
		}
		for i := range args {
			args[i] = strings.ReplaceAll(strings.ReplaceAll(args[i], "{audio}", name), "{model_dir}", model)
		}
		out, e := exec.CommandContext(ctx, exe, args...).CombinedOutput()
		if e != nil {
			return Transcript{}, errors.New("media: local transcription failed")
		}
		text := strings.TrimSpace(string(out))
		return Transcript{Text: text, Elapsed: time.Since(start), Duration: a.Duration, RTF: rtf(time.Since(start), a.Duration)}, nil
	}
	if provider == "cloud" {
		return p.cloudTranscribe(ctx, a, name, start)
	}
	return Transcript{}, ErrUnsupported
}
func (p *Processor) cloudTranscribe(ctx context.Context, a Attachment, name string, start time.Time) (Transcript, error) {
	endpoint, _ := p.cfg["asr.cloud_url"].(string)
	key, _ := p.cfg["asr.cloud_key"].(string)
	model, _ := p.cfg["asr.cloud_model"].(string)
	if endpoint == "" || key == "" {
		return Transcript{}, ErrNotConfigured
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" {
		return Transcript{}, ErrUnsafeURL
	}
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	_ = mw.WriteField("model", model)
	part, e := mw.CreateFormFile("file", safeName(a.Name))
	if e != nil {
		return Transcript{}, e
	}
	if _, e = part.Write(a.Data); e != nil {
		return Transcript{}, e
	}
	mw.Close()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &b)
	if e != nil {
		return Transcript{}, e
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, e := p.client.Do(req)
	if e != nil {
		return Transcript{}, e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Transcript{}, errors.New("media: cloud transcription failed")
	}
	var v struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v) != nil {
		return Transcript{}, errors.New("media: invalid transcription response")
	}
	return Transcript{Text: v.Text, Language: v.Language, Elapsed: time.Since(start), Duration: a.Duration, RTF: rtf(time.Since(start), a.Duration)}, nil
}
func (p *Processor) Describe(ctx context.Context, as []Attachment, prompt string) (string, error) {
	return "", ErrNotConfigured
}
func rtf(elapsed time.Duration, duration float64) float64 {
	if duration <= 0 {
		return 0
	}
	return elapsed.Seconds() / duration
}
func safeName(s string) string {
	s = filepath.Base(strings.ReplaceAll(s, "\\", "/"))
	if s == "." || s == "" {
		return "attachment.bin"
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:\"/|?*`, r) {
			return '_'
		}
		return r
	}, s)
}

type downloadResolver func(context.Context, string, string) ([]net.IP, error)

type downloadTransport interface {
	http.RoundTripper
	CloseIdleConnections()
}

func SafeDownload(ctx context.Context, raw string, maxBytes int64) ([]byte, error) {
	return safeDownload(ctx, raw, maxBytes, net.DefaultResolver.LookupIP, newDownloadTransport)
}

func safeDownload(ctx context.Context, raw string, maxBytes int64, resolve downloadResolver, transport func([]net.IP) downloadTransport) ([]byte, error) {
	// One deadline includes DNS, connection setup, headers and the response body.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, ErrUnsafeURL
	}
	if maxBytes <= 0 || maxBytes > 128<<20 {
		maxBytes = 40 << 20
	}
	ips, e := resolve(ctx, "ip", u.Hostname())
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e != nil || len(ips) == 0 {
		return nil, ErrUnsafeURL
	}
	for _, ip := range ips {
		if !downloadPublicIP(ip) {
			return nil, ErrUnsafeURL
		}
	}
	tr := transport(ips)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, ErrUnsafeURL
	}
	resp, e := client.Do(req)
	if e != nil {
		return nil, downloadError(ctx)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("media: download failed")
	}
	if resp.ContentLength > maxBytes {
		return nil, ErrTooLarge
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	if e != nil || ctx.Err() != nil {
		return nil, downloadError(ctx)
	}
	return data, nil
}

func downloadError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Transport and body errors can include the request URL and its token.
	return errors.New("media: download failed")
}

func downloadPublicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// Shared address space (CGNAT) is not public; neither are 0/8 or 240/4.
		return v4[0] != 0 && v4[0] < 240 && !(v4[0] == 100 && v4[1]&0xc0 == 64)
	}
	return true
}

func newDownloadTransport(ips []net.IP) downloadTransport {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	// Build a private transport: no environment proxy, inherited TLS override,
	// alternate protocol handler or connection pool may bypass the pinned dialer.
	return &http.Transport{
		DialContext:           downloadDialContext(ips, dialer.DialContext),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives:     true,
	}
}

func downloadDialContext(ips []net.IP, dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	// Copy the validated values so subsequent resolver state cannot change them.
	addresses := make([]string, len(ips))
	for i, ip := range ips {
		addresses[i] = ip.String()
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("media: public address unavailable")
		}
		for _, ip := range addresses {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			conn, err := dial(ctx, network, net.JoinHostPort(ip, port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, errors.New("media: public address unavailable")
	}
}

func SHA256(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
