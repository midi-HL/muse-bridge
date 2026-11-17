package hatch

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"
)

type stream struct {
	mu     sync.Mutex
	status int
	data   []byte
	ended  bool
	err    error
	notify chan struct{}
}

func (s *stream) push(p packet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return fault("protocol", "stream", "data after stream end")
	}
	if p.status != 0 {
		// Hatch streams may repeat the HTTP status on a later data packet. It
		// must agree with the first header; a mismatch is a protocol error.
		if s.status != 0 && s.status != p.status {
			return fault("protocol", "stream", "conflicting response headers")
		}
		if s.status == 0 {
			s.status = p.status
		}
	} else if s.status == 0 && !p.reset {
		return fault("protocol", "stream", "body arrived before headers")
	}
	if len(s.data)+len(p.data) > maxResponseBytes {
		return fault("protocol", "stream", "response buffer limit exceeded")
	}
	s.data = append(s.data, p.data...)
	s.ended = p.end
	if p.reset {
		s.err = fault("protocol", "stream", "upstream reset the logical stream")
	}
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return nil
}

// take drains data while preserving status/end. The receive loop never waits on
// application code; exceeding the bound fails the connection explicitly.
func (s *stream) take() (int, []byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.data
	s.data = nil
	return s.status, d, s.ended, s.err
}

type connection struct {
	ws       *websocket.Conn
	tx, rx   *noise.CipherState
	ctx      context.Context
	cancel   context.CancelFunc
	sendGate chan struct{}
	mu       sync.Mutex
	streams  map[uint64]*stream
	next     uint64
	dead     chan struct{}
	err      error
	once     sync.Once
}

func (cn *connection) fail(err error) {
	cn.once.Do(func() {
		cn.mu.Lock()
		cn.err = err
		close(cn.dead)
		cn.mu.Unlock()
		cn.cancel()
		_ = cn.ws.CloseNow()
	})
}
func (cn *connection) failure() error {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	if cn.err != nil {
		return cn.err
	}
	return fault("network", "transport", "connection closed")
}
func (cn *connection) receive() {
	var r reassembler
	for {
		typ, data, err := cn.ws.Read(cn.ctx)
		if err != nil {
			cn.fail(contextError("receive", cn.ctx.Err()))
			return
		}
		if typ != websocket.MessageBinary {
			cn.fail(fault("protocol", "receive", "unexpected text WebSocket frame"))
			return
		}
		plain, err := cn.rx.Decrypt(nil, nil, data)
		if err != nil {
			cn.fail(fault("protocol", "receive", "Noise frame authentication failed"))
			return
		}
		raw, err := r.push(plain, time.Now())
		if err != nil {
			cn.fail(err)
			return
		}
		if raw == nil {
			continue
		}
		p, err := decodeResponse(raw)
		if err != nil {
			cn.fail(err)
			return
		}
		cn.mu.Lock()
		s := cn.streams[p.id]
		cn.mu.Unlock()
		// A bounded request may have been cancelled; late packets are discarded and
		// never allocate a response for an unsolicited stream ID.
		if s != nil {
			if err = s.push(p); err != nil {
				cn.fail(err)
				return
			}
		}
	}
}
func (cn *connection) send(ctx context.Context, raw []byte) error {
	if err := acquire(ctx, cn.sendGate); err != nil {
		return err
	}
	defer func() { cn.sendGate <- struct{}{} }()
	select {
	case <-cn.dead:
		return cn.failure()
	default:
	}
	frames, err := pack(raw)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		encrypted, e := cn.tx.Encrypt(nil, nil, frame)
		if e != nil {
			err = fault("protocol", "send", "Noise encryption failed")
			cn.fail(err)
			return err
		}
		if e = cn.ws.Write(ctx, websocket.MessageBinary, encrypted); e != nil {
			err = contextError("send", ctx.Err())
			cn.fail(err)
			return err
		}
	}
	return nil
}
func (cn *connection) open(ctx context.Context, method, path string, body any) (uint64, *stream, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return 0, nil, fault("protocol", "request", "invalid request body")
		}
	}
	if len(data) > maxLogicalFrame-1024 {
		return 0, nil, fault("protocol", "request", "request exceeds message limit")
	}
	cn.mu.Lock()
	if cn.err != nil {
		err = cn.err
		cn.mu.Unlock()
		return 0, nil, err
	}
	if len(cn.streams) >= maxPendingStreams {
		cn.mu.Unlock()
		return 0, nil, fault("rate_limit", "request", "too many concurrent logical streams")
	}
	id := cn.next
	cn.next++
	s := &stream{notify: make(chan struct{}, 1)}
	cn.streams[id] = s
	cn.mu.Unlock()
	if err = cn.send(ctx, encodeRequest(id, method, path, data)); err != nil {
		cn.release(id, false)
		return 0, nil, err
	}
	return id, s, nil
}
func (cn *connection) release(id uint64, reset bool) {
	cn.mu.Lock()
	delete(cn.streams, id)
	cn.mu.Unlock()
	if reset {
		ctx, cancel := context.WithTimeout(cn.ctx, 100*time.Millisecond)
		defer cancel()
		_ = cn.send(ctx, encodeReset(id))
	}
}
func (cn *connection) wait(ctx context.Context, s *stream) error {
	select {
	case <-ctx.Done():
		return contextError("stream", ctx.Err())
	case <-cn.dead:
		return cn.failure()
	case <-s.notify:
		return nil
	}
}
func (cn *connection) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	id, s, err := cn.open(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() { cn.release(id, !complete) }()
	var out []byte
	status := 0
	for {
		st, data, end, se := s.take()
		if st != 0 {
			status = st
		}
		if se != nil {
			return nil, se
		}
		if len(out)+len(data) > maxResponseBytes {
			return nil, fault("protocol", "request", "response exceeds limit")
		}
		out = append(out, data...)
		if status != 0 && (status < 200 || status >= 300) {
			return nil, statusError("request", status)
		}
		if end {
			complete = true
			if status == 0 {
				return nil, fault("protocol", "request", "response ended without headers")
			}
			return out, nil
		}
		if err = cn.wait(ctx, s); err != nil {
			return nil, err
		}
	}
}
func (c *Client) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	ctx, cancel := c.operation(ctx, 90*time.Second)
	defer cancel()
	cn, err := c.current(ctx)
	if err != nil {
		return nil, err
	}
	return cn.request(ctx, method, path, body)
}
func (cn *connection) heartbeat() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-cn.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(cn.ctx, 15*time.Second)
			_, err := cn.request(ctx, "POST", "/api/ping", map[string]any{"presence": "active"})
			cancel()
			if err != nil {
				cn.fail(err)
				return
			}
		}
	}
}
