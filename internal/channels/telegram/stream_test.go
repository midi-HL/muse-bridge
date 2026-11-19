package telegram

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type recordedTelegramCall struct {
	method string
	values url.Values
}
type streamRoundTrip struct {
	calls []recordedTelegramCall
	next  int
}

func (s *streamRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	_ = r.ParseForm()
	s.calls = append(s.calls, recordedTelegramCall{method: r.URL.Path, values: r.Form})
	s.next++
	body := `{"ok":true,"result":{"message_id":1}}`
	if strings.HasSuffix(r.URL.Path, "editMessageText") || strings.HasSuffix(r.URL.Path, "deleteMessage") {
		body = `{"ok":true,"result":true}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}
func newStreamTest(t *testing.T) (*StreamEditor, *streamRoundTrip) {
	t.Helper()
	rt := &streamRoundTrip{}
	client, err := NewClient("123:secret", &http.Client{Transport: rt})
	if err != nil {
		t.Fatal(err)
	}
	return NewStreamEditor(client, 42, 0), rt
}
func TestStreamEditorStartsPlaceholderThenEditsSingleMessage(t *testing.T) {
	s, rt := newStreamTest(t)
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rt.calls) != 1 || !strings.HasSuffix(rt.calls[0].method, "sendMessage") || rt.calls[0].values.Get("text") == "" {
		t.Fatalf("placeholder not sent: %#v", rt.calls)
	}
	if err := s.Push(ctx, "hello "); err != nil {
		t.Fatal(err)
	}
	if err := s.Push(ctx, "world"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if s.Content() != "hello world" {
		t.Fatalf("content=%q", s.Content())
	}
	sent := 0
	edited := 0
	for _, c := range rt.calls {
		if strings.HasSuffix(c.method, "sendMessage") {
			sent++
		}
		if strings.HasSuffix(c.method, "editMessageText") {
			edited++
		}
	}
	if sent != 1 || edited < 2 {
		t.Fatalf("expected one message and edits, sent=%d edited=%d calls=%#v", sent, edited, rt.calls)
	}
}
func TestStreamEditorDeletesEmptyPlaceholderOnCloseOrAbort(t *testing.T) {
	for _, abort := range []bool{false, true} {
		s, rt := newStreamTest(t)
		ctx := context.Background()
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		var err error
		if abort {
			err = s.Abort(ctx)
		} else {
			err = s.Close(ctx, "")
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(rt.calls) != 2 || !strings.HasSuffix(rt.calls[1].method, "deleteMessage") {
			t.Fatalf("placeholder not deleted: %#v", rt.calls)
		}
	}
}
func TestStreamEditorSplitsLongUTF16Text(t *testing.T) {
	s, rt := newStreamTest(t)
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("😀", 3000)
	if err := s.Push(ctx, text); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx, ""); err != nil {
		t.Fatal(err)
	}
	count := 0
	seen := map[string]bool{}
	for _, c := range rt.calls {
		if strings.HasSuffix(c.method, "sendMessage") || strings.HasSuffix(c.method, "editMessageText") {
			part := c.values.Get("text")
			if telegramUnits(part) > telegramTextLimit {
				t.Fatalf("Telegram payload exceeded limit: %d", telegramUnits(part))
			}
			if !strings.Contains(part, streamPlaceholder) && !seen[part] {
				count += telegramUnits(part)
				seen[part] = true
			}
		}
	}
	if count != telegramUnits(text) {
		t.Fatalf("split content size mismatch: got %d want %d", count, telegramUnits(text))
	}
}
func TestSplitTelegramChunkCountsUTF16(t *testing.T) {
	text := strings.Repeat("😀", 3000)
	prefix, rest := splitTelegramChunk(text, telegramTextLimit)
	if telegramUnits(prefix) > telegramTextLimit || rest == "" {
		t.Fatalf("bad chunk: prefix=%d rest=%d", telegramUnits(prefix), telegramUnits(rest))
	}
	if telegramUnits(prefix)+telegramUnits(rest) != telegramUnits(text) {
		t.Fatal("chunk lost content")
	}
}

func TestStreamEditorPreservesTailAfterOverflow(t *testing.T) {
	for _, next := range []string{"", "more"} {
		s, rt := newStreamTest(t)
		ctx := context.Background()
		if err := s.Push(ctx, strings.Repeat("x", telegramTextLimit)+"tail"); err != nil {
			t.Fatal(err)
		}
		if next != "" {
			if err := s.Push(ctx, next); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(ctx, ""); err != nil {
			t.Fatal(err)
		}
		last := rt.calls[len(rt.calls)-1]
		if strings.HasSuffix(last.method, "deleteMessage") || last.values.Get("text") != "tail"+next {
			t.Fatalf("last message = %#v; want remaining tail %q", last, "tail"+next)
		}
	}
}

func TestStreamEditorPreservesPlainTextSymbols(t *testing.T) {
	for _, text := range []string{"x < y & z > q", strings.Repeat("<&>", 2000)} {
		s, rt := newStreamTest(t)
		ctx := context.Background()
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.Push(ctx, text); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(ctx, text); err != nil {
			t.Fatal(err)
		}
		var delivered strings.Builder
		for _, call := range rt.calls {
			if call.values.Get("text") == streamPlaceholder {
				continue
			}
			if call.values.Get("parse_mode") != "" {
				t.Fatalf("plain text reply unexpectedly used parse_mode %q", call.values.Get("parse_mode"))
			}
			delivered.WriteString(call.values.Get("text"))
		}
		if delivered.String() != text {
			t.Fatalf("stream altered symbols: delivered %d bytes, want original %d bytes", delivered.Len(), len(text))
		}
	}
}

func TestCommandAndScheduledTextPreserveSymbolsWhileSplitting(t *testing.T) {
	text := strings.Repeat("<&>", 2000)
	for _, command := range []bool{false, true} {
		s, rt := newStreamTest(t)
		ch := &Channel{client: s.client, handler: Callbacks{OnCommand: func(context.Context, Request, Command) (Response, error) { return Response{Text: text}, nil }}}
		var err error
		if command {
			err = ch.handleRequest(context.Background(), Request{ChatID: 42, UserID: 7, Text: "/status"})
		} else {
			err = ch.SendText(context.Background(), 42, text)
		}
		if err != nil {
			t.Fatal(err)
		}
		var delivered strings.Builder
		for _, call := range rt.calls {
			if call.values.Get("parse_mode") != "" {
				t.Fatal("plain response unexpectedly used HTML mode")
			}
			delivered.WriteString(call.values.Get("text"))
		}
		if delivered.String() != text {
			t.Fatal("long reply changed or lost original symbols")
		}
	}
}
