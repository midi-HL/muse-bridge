package telegram

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestImportStickerSetRejectsUnsafeURLs(t *testing.T) {
	for _, raw := range []string{
		"http://t.me/addstickers/demo",
		"https://evil.example/addstickers/demo",
		"https://t.me/addstickers/../secret",
		"https://t.me/addstickers/demo?token=secret",
		"https://t.me/addstickers/demo/extra",
		"https://t.me/addstickers/demo%2Fsecret",
		"https://t.me.evil.example/addstickers/demo",
		"https://t.me:443/addstickers/demo",
	} {
		if _, err := ImportStickerSetContext(context.Background(), "123:secret", raw, nil); err == nil {
			t.Fatalf("unsafe URL accepted: %q", raw)
		}
	}
}

func TestImportStickerSetRequiresToken(t *testing.T) {
	_, err := ImportStickerSetContext(context.Background(), "", "https://t.me/addstickers/demo", nil)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestImportStickerSetReportsPartialItems(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/getStickerSet") {
			t.Fatalf("unexpected API path: %s", r.URL.Path)
		}
		body := `{"ok":true,"result":{"name":"demo_by_bot","stickers":[{"file_id":"f1","file_unique_id":"u1","emoji":"😀"},{"file_unique_id":"u2","emoji":"😢"}]}}`
		return &http.Response{StatusCode: 200, Body: ioNopCloser{strings.NewReader(body)}, Header: make(http.Header)}, nil
	})
	report, err := ImportStickerSetContext(context.Background(), "123:secret", "https://t.me/addstickers/demo_by_bot", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 2 || report.Imported != 1 || len(report.Failed) != 1 || report.Assets[0].FileID != "f1" {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestImportStickerSetAPIErrorIsGeneric(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"ok":false,"description":"bad token 123:secret"}`
		return &http.Response{StatusCode: 200, Body: ioNopCloser{strings.NewReader(body)}, Header: make(http.Header)}, nil
	})
	_, err := ImportStickerSetContext(context.Background(), "123:secret", "https://t.me/addstickers/demo", &http.Client{Transport: transport})
	if err == nil || !strings.Contains(err.Error(), "API request failed") || strings.Contains(err.Error(), "123:secret") {
		t.Fatalf("unexpected API error: %v", err)
	}
}

type ioNopCloser struct{ *strings.Reader }

func (ioNopCloser) Close() error { return nil }
