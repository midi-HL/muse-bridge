package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type pollingTransport func(*http.Request) (*http.Response, error)

func (f pollingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func pollingResponse(value any) *http.Response {
	b, _ := json.Marshal(map[string]any{"ok": true, "result": value})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}
}
func pollingUpdate(id int, text string) map[string]any {
	return map[string]any{"update_id": id, "message": map[string]any{"message_id": id, "from": map[string]any{"id": 7}, "chat": map[string]any{"id": 42, "type": "private"}, "text": text}}
}

func TestPollingDeliversStopDuringChatAndRetry(t *testing.T) {
	for _, input := range []string{"normal prompt", "/retry"} {
		t.Run(input, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, stopped, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var polls atomic.Int32
			httpClient := &http.Client{Transport: pollingTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "getUpdates") {
					switch polls.Add(1) {
					case 1:
						return pollingResponse([]any{pollingUpdate(1, input)}), nil
					case 2:
						select {
						case <-started:
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
						return pollingResponse([]any{pollingUpdate(2, "/stop@test_bot")}), nil
					default:
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
				}
				if err := r.Context().Err(); err != nil {
					return nil, err
				}
				return pollingResponse(map[string]any{"message_id": 1}), nil
			})}
			client, err := NewClient("123:synthetic", httpClient)
			if err != nil {
				t.Fatal(err)
			}
			wait := func(run context.Context) (Response, error) {
				close(started)
				<-run.Done()
				close(ended)
				return Response{}, run.Err()
			}
			handler := Callbacks{OnChat: func(run context.Context, _ Request, _ func(string) error) (Response, error) { return wait(run) }, OnCommand: func(run context.Context, _ Request, c Command) (Response, error) {
				if c.Name == "retry" {
					return wait(run)
				}
				if c.Name != "stop" {
					return Response{}, errors.New("incorrect group command name")
				}
				close(stopped)
				return Response{Text: "stopped"}, nil
			}}
			channel := &Channel{client: client, handler: handler, mode: "polling", botUsername: "test_bot", whitelist: map[int64]bool{}, poll: time.Millisecond}
			finished := make(chan error, 1)
			go func() { finished <- channel.Run(ctx) }()
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("polling blocked /stop behind model response")
			}
			select {
			case <-ended:
			case <-time.After(3 * time.Second):
				t.Fatal("long task context not cancelled")
			}
			cancel()
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Run ended with %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Run leaked dispatched task")
			}
		})
	}
}

func TestParseTelegramCommandPreservesArgsAndBotSuffix(t *testing.T) {
	command, ok := parseTelegramCommand("  /persona@test_bot calm style  ")
	if !ok || command.Name != "persona" || command.Args != "calm style" {
		t.Fatalf("command=%#v ok=%v", command, ok)
	}
}
