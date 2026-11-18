package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type abortRoundTrip func(*http.Request) (*http.Response, error)

func (f abortRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestStreamAbortDeletesPlaceholderAfterGenerationCancel(t *testing.T) {
	var deleted bool
	client, err := NewClient("123:secret", &http.Client{Transport: abortRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":5}}`)), Header: make(http.Header)}, nil
		}
		if strings.HasSuffix(r.URL.Path, "deleteMessage") {
			if r.Context().Err() != nil {
				t.Fatalf("cleanup used canceled context: %v", r.Context().Err())
			}
			deleted = true
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`)), Header: make(http.Header)}, nil
		}
		return nil, errors.New("unexpected method")
	})})
	if err != nil {
		t.Fatal(err)
	}
	s := NewStreamEditor(client, 42, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err = s.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("placeholder was not deleted")
	}
}
