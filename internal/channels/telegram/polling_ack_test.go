package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func pollingAckUpdate(id int, chat int64, text string) map[string]any {
	u := pollingUpdate(id, text)
	u["message"].(map[string]any)["chat"].(map[string]any)["id"] = chat
	return u
}

func pollingAckFailure() *http.Response {
	return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"description":"Forbidden"}`))}
}

func newPollingAckChannel(t *testing.T, handler Handler, transport pollingTransport) *Channel {
	t.Helper()
	client, err := NewClient("123:synthetic", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return &Channel{client: client, handler: handler, mode: "polling", poll: time.Millisecond}
}

func TestPollingAckContinuesAfterControlReplyFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var commands []string
	var offsets []string
	var sendAttempts, newEffects int
	newCached := false
	ch := newPollingAckChannel(t, Callbacks{OnCommand: func(_ context.Context, r Request, c Command) (Response, error) {
		commands = append(commands, strconv.FormatInt(r.ChatID, 10)+":"+c.Name)
		if c.Name == "new" {
			if !newCached {
				newCached = true
				newEffects++
			}
			return Response{Text: "new session"}, nil
		}
		return Response{}, nil
	}}, func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			offsets = append(offsets, r.Form.Get("offset"))
			switch len(offsets) {
			case 1:
				return pollingResponse([]any{pollingAckUpdate(40, 42, "/new"), pollingAckUpdate(44, 99, "/stop")}), nil
			case 2:
				return pollingResponse([]any{pollingAckUpdate(40, 42, "/new")}), nil
			default:
				cancel()
				return nil, r.Context().Err()
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sendAttempts++
			if sendAttempts == 1 {
				return pollingAckFailure(), nil
			}
			return pollingResponse(map[string]any{"message_id": 1}), nil
		default:
			return nil, errors.New("unexpected Bot API method")
		}
	})
	if err := ch.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	if sendAttempts != 2 || newEffects != 1 || !reflect.DeepEqual(commands, []string{"42:new", "99:stop", "42:new"}) || !reflect.DeepEqual(offsets, []string{"0", "40", "45"}) {
		t.Fatalf("send attempts=%d, new effects=%d, callbacks=%v, offsets=%v; want one failed and one successful delivery, one business effect, callbacks [42:new 99:stop 42:new], offsets [0 40 45]", sendAttempts, newEffects, commands, offsets)
	}
}

func TestPollingAckAdvancesAfterPermanentReplyFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var offsets []string
	var attempts, stops int
	ch := newPollingAckChannel(t, Callbacks{OnCommand: func(_ context.Context, _ Request, cmd Command) (Response, error) {
		if cmd.Name == "stop" {
			stops++
			return Response{}, nil
		}
		return Response{Text: "new session"}, nil
	}}, func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			attempts++
			return pollingAckFailure(), nil
		}
		offsets = append(offsets, r.Form.Get("offset"))
		if len(offsets) <= 2 {
			return pollingResponse([]any{map[string]any{"update_id": 39}, pollingAckUpdate(40, 42, "/new"), pollingAckUpdate(44, 99, "/stop")}), nil
		}
		cancel()
		return nil, r.Context().Err()
	})
	if err := ch.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	if attempts != 2 || stops != 1 || !reflect.DeepEqual(offsets, []string{"0", "40", "45"}) {
		t.Fatalf("send attempts=%d, stops=%d, offsets=%v", attempts, stops, offsets)
	}
}

func TestPollingAckContinuesAfterBusyReplyFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var offsets []string
	var stops, failedSends int
	ch := newPollingAckChannel(t, Callbacks{OnCommand: func(ctx context.Context, _ Request, c Command) (Response, error) {
		if c.Name == "retry" {
			<-ctx.Done()
			return Response{}, ctx.Err()
		}
		if c.Name == "stop" {
			stops++
		}
		return Response{}, nil
	}}, func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			offsets = append(offsets, r.Form.Get("offset"))
			if len(offsets) == 1 {
				return pollingResponse([]any{pollingUpdate(10, "/retry"), pollingUpdate(12, "busy prompt"), pollingAckUpdate(13, 99, "/stop")}), nil
			}
			cancel()
			return nil, r.Context().Err()
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") && strings.Contains(r.Form.Get("text"), "/stop") {
			failedSends++
			return pollingAckFailure(), nil
		}
		return nil, errors.New("unexpected Bot API call")
	})
	if err := ch.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	if failedSends != 1 || stops != 1 || !reflect.DeepEqual(offsets, []string{"0", "10"}) {
		t.Fatalf("failed busy replies=%d, stop calls=%d, offsets=%v; want 1, 1, [0 10]", failedSends, stops, offsets)
	}
}

type pollingAckBinder struct {
	Callbacks
	bind func(Request) Request
}

func (b pollingAckBinder) BindSession(_ context.Context, req Request) (Request, error) {
	return b.bind(req), nil
}

func TestPollingAckPendingRedeliveryAndSameBatchBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	selected := "old-session"
	var offsets []string
	var bindings int
	var chats, newCommands atomic.Int32
	switched := make(chan struct{})
	started := make(chan string, 1)
	items := []any{pollingUpdate(41, "prompt"), pollingUpdate(44, "/new")}
	handler := pollingAckBinder{bind: func(req Request) Request {
		bindings++
		req.SessionID = selected
		return req
	}, Callbacks: Callbacks{
		OnChat: func(ctx context.Context, r Request, _ func(string) error) (Response, error) {
			if chats.Add(1) == 1 {
				started <- r.SessionID
			}
			<-ctx.Done()
			return Response{}, ctx.Err()
		},
		OnCommand: func(_ context.Context, _ Request, c Command) (Response, error) {
			if c.Name == "new" && newCommands.Add(1) == 1 {
				selected = "new-session"
				close(switched)
			}
			return Response{}, nil
		},
	}}
	var session string
	ch := newPollingAckChannel(t, handler, func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			offsets = append(offsets, r.Form.Get("offset"))
			switch len(offsets) {
			case 1:
				return pollingResponse(items), nil
			case 2:
				select {
				case session = <-started:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				// Replay both the pending worker and the completed control command.
				return pollingResponse(items), nil
			default:
				cancel()
				return nil, r.Context().Err()
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			// The chat callback cannot start until /new has switched the pointer.
			select {
			case <-switched:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return pollingResponse(map[string]any{"message_id": 1}), nil
	})
	if err := ch.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	if session != "old-session" || selected != "new-session" || bindings != 2 || chats.Load() != 1 || newCommands.Load() != 1 || !reflect.DeepEqual(offsets, []string{"0", "41", "41"}) {
		t.Fatalf("session=%q, selected=%q, bindings=%d, chat callbacks=%d, /new callbacks=%d, offsets=%v; want old binding, two bindings, single calls and [0 41 41]", session, selected, bindings, chats.Load(), newCommands.Load(), offsets)
	}
}

func TestPollingAckShutdownBeforeCallbackAllowsNextRunToReceiveUpdate(t *testing.T) {
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelFirst()
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelSecond()
	const id = 53
	items := []any{pollingUpdate(id, "interrupted prompt")}
	placeholderStarted := make(chan struct{})
	var chats atomic.Int32
	var firstOffsets, secondOffsets []int
	secondRun, confirmed := false, false
	ch := newPollingAckChannel(t, Callbacks{OnChat: func(context.Context, Request, func(string) error) (Response, error) {
		chats.Add(1)
		return Response{Text: "finished"}, nil
	}}, func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
			offset, err := strconv.Atoi(r.Form.Get("offset"))
			if err != nil {
				return nil, err
			}
			// The fake Telegram server retains the update until a poll confirms it.
			confirmed = confirmed || offset > id
			if !secondRun {
				firstOffsets = append(firstOffsets, offset)
				if len(firstOffsets) == 2 {
					select {
					case <-placeholderStarted:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
					cancelFirst()
					return nil, r.Context().Err()
				}
			} else {
				secondOffsets = append(secondOffsets, offset)
				if confirmed {
					cancelSecond()
					return nil, r.Context().Err()
				}
			}
			return pollingResponse(items), nil
		case strings.HasSuffix(r.URL.Path, "/sendMessage") && !secondRun:
			close(placeholderStarted)
			<-r.Context().Done()
			return nil, r.Context().Err()
		default:
			return pollingResponse(map[string]any{"message_id": 1}), nil
		}
	})
	if err := ch.Run(firstCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run returned %v", err)
	}
	if chats.Load() != 0 || confirmed || !reflect.DeepEqual(firstOffsets, []int{0, id}) {
		t.Errorf("first Run: callback count=%d, confirmed=%v, offsets=%v; want 0, false, [0 %d]", chats.Load(), confirmed, firstOffsets, id)
	}
	secondRun = true
	if err := ch.Run(secondCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Run returned %v", err)
	}
	if chats.Load() != 1 || len(secondOffsets) < 2 || secondOffsets[len(secondOffsets)-1] != id+1 {
		t.Fatalf("second Run: callback count=%d, offsets=%v; want one callback followed by offset %d", chats.Load(), secondOffsets, id+1)
	}
}

func TestPollingAckRetriesBusinessFailureWithoutRepeatingLaterCommands(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var offsets, commands []string
	var attempts int
	items := []any{pollingUpdate(10, "/new"), pollingAckUpdate(13, 99, "/stop")}
	ch := newPollingAckChannel(t, Callbacks{OnCommand: func(_ context.Context, _ Request, cmd Command) (Response, error) {
		commands = append(commands, cmd.Name)
		if cmd.Name == "new" {
			attempts++
			if attempts == 1 {
				return Response{}, errors.New("command callback failed")
			}
		}
		return Response{}, nil
	}}, func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			return nil, errors.New("unexpected Bot API call")
		}
		offsets = append(offsets, r.Form.Get("offset"))
		if len(offsets) <= 2 {
			return pollingResponse(items), nil
		}
		cancel()
		return nil, r.Context().Err()
	})
	if err := ch.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	if !reflect.DeepEqual(commands, []string{"new", "stop", "new"}) || !reflect.DeepEqual(offsets, []string{"0", "10", "14"}) {
		t.Fatalf("commands=%v, offsets=%v; want [new stop new], [0 10 14]", commands, offsets)
	}
}

func TestPollingAckAdvancesAfterStopCancelsWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var offsets []string
	var retries, stops atomic.Int32
	var workerErr error
	ch := newPollingAckChannel(t, Callbacks{OnCommand: func(ctx context.Context, _ Request, c Command) (Response, error) {
		switch c.Name {
		case "retry":
			retries.Add(1)
			<-ctx.Done()
			workerErr = ctx.Err()
			return Response{}, workerErr
		case "stop":
			stops.Add(1)
		}
		return Response{}, nil
	}}, func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		offset := r.Form.Get("offset")
		offsets = append(offsets, offset)
		switch len(offsets) {
		case 1:
			return pollingResponse([]any{pollingUpdate(11, "/retry")}), nil
		case 2:
			return pollingResponse([]any{pollingUpdate(20, "/stop")}), nil
		}
		if offset != "11" {
			cancel()
			return nil, r.Context().Err()
		}
		// Keep serving the unconfirmed updates until the canceled job finishes.
		return pollingResponse([]any{pollingUpdate(11, "/retry"), pollingUpdate(20, "/stop")}), nil
	})
	if err := ch.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v at offset %v", err, offsets)
	}
	if retries.Load() != 1 || stops.Load() != 1 || !errors.Is(workerErr, context.Canceled) || len(offsets) < 3 || offsets[1] != "11" || offsets[len(offsets)-1] != "21" {
		t.Fatalf("retry calls=%d, stop calls=%d, worker error=%v, offsets=%v; want single calls and offsets 0, 11...21", retries.Load(), stops.Load(), workerErr, offsets)
	}
}
