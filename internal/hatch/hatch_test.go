package hatch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"
)

type mockHatch struct {
	mu              sync.Mutex
	files           map[string][]byte
	paths           []string
	wsURL           string
	server          *httptest.Server
	wsReady         chan struct{}
	delayEvents     bool
	wsErr           chan error
	subscribeEvents []map[string]any
	streamEvents    func(string) []map[string]any
}

func newMockHatch(t *testing.T) *mockHatch {
	t.Helper()
	m := &mockHatch{files: make(map[string][]byte), wsReady: make(chan struct{}), wsErr: make(chan error, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "hatch_vml=renewed; Max-Age=3600")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"assigned","vm_state":"RUNNING","vm_id":"vm-test","endpoint_url":"https://vm.test","vm_name":"vm"}`))
	})
	mux.HandleFunc("/api/hatch/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"test-token"}`))
	})
	mux.HandleFunc("/api/hatch/vm/wake", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/mock-files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/fs/write", m.fsWrite)
	mux.HandleFunc("/fs/stat", m.fsStat)
	mux.HandleFunc("/fs/rename", m.fsRename)
	mux.HandleFunc("/fs/read", m.fsRead)
	mux.HandleFunc("/fs/delete", m.fsDelete)
	mux.HandleFunc("/v1/noise", m.wsHandler)
	m.server = httptest.NewServer(mux)
	m.wsURL = "ws" + strings.TrimPrefix(m.server.URL, "http") + "/v1/noise"
	return m
}
func (m *mockHatch) close()             { m.server.Close() }
func (m *mockHatch) record(path string) { m.mu.Lock(); m.paths = append(m.paths, path); m.mu.Unlock() }
func (m *mockHatch) body(r *http.Request) map[string]any {
	var v map[string]any
	_ = json.NewDecoder(r.Body).Decode(&v)
	return v
}
func (m *mockHatch) fsWrite(w http.ResponseWriter, r *http.Request) {
	v := m.body(r)
	p, _ := v["path"].(string)
	d, _ := v["data_base64"].(string)
	b, _ := base64.StdEncoding.DecodeString(d)
	m.mu.Lock()
	if overwrite, _ := v["overwrite"].(bool); overwrite {
		m.files[p] = nil
	}
	m.files[p] = append(m.files[p], b...)
	n := len(m.files[p])
	m.mu.Unlock()
	writeJSON(w, map[string]any{"kind": "file", "size": n})
}
func (m *mockHatch) fsStat(w http.ResponseWriter, r *http.Request) {
	v := m.body(r)
	p, _ := v["path"].(string)
	m.mu.Lock()
	b, ok := m.files[p]
	m.mu.Unlock()
	if !ok {
		http.Error(w, "missing", 404)
		return
	}
	writeJSON(w, map[string]any{"kind": "file", "size": len(b)})
}
func (m *mockHatch) fsRename(w http.ResponseWriter, r *http.Request) {
	v := m.body(r)
	src, _ := v["source"].(string)
	dst, _ := v["destination"].(string)
	m.mu.Lock()
	m.files[dst] = m.files[src]
	delete(m.files, src)
	m.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}
func (m *mockHatch) fsDelete(w http.ResponseWriter, r *http.Request) {
	v := m.body(r)
	p, _ := v["path"].(string)
	m.mu.Lock()
	delete(m.files, p)
	m.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}
func (m *mockHatch) fsRead(w http.ResponseWriter, r *http.Request) {
	v := m.body(r)
	p, _ := v["path"].(string)
	off := int(number(v["offset"]))
	n := int(number(v["len"]))
	m.mu.Lock()
	b := append([]byte(nil), m.files[p]...)
	m.mu.Unlock()
	if off > len(b) {
		off = len(b)
	}
	end := off + n
	if end > len(b) {
		end = len(b)
	}
	chunk := b[off:end]
	writeJSON(w, map[string]any{"data_base64": base64.StdEncoding.EncodeToString(chunk), "len": len(chunk), "eof": end == len(b)})
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func number(v any) float64 { n, _ := v.(float64); return n }
func (m *mockHatch) wsHandler(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	closeOnce(m.wsReady)
	suite := noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256)
	static, _ := suite.GenerateKeypair(nil)
	hs, _ := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeXX, Initiator: false, StaticKeypair: static})
	ctx := r.Context()
	typ, msg, e := ws.Read(ctx)
	if e != nil || typ != websocket.MessageBinary {
		return
	}
	if _, _, _, e = hs.ReadMessage(nil, msg); e != nil {
		return
	}
	msg, _, _, e = hs.WriteMessage(nil, nil)
	if e != nil {
		return
	}
	if e = ws.Write(ctx, websocket.MessageBinary, msg); e != nil {
		return
	}
	typ, msg, e = ws.Read(ctx)
	if e != nil || typ != websocket.MessageBinary {
		return
	}
	_, rx, tx, e := hs.ReadMessage(nil, msg)
	if e != nil {
		return
	}
	var reass reassembler
	subID := uint64(0)
	for {
		typ, msg, e = ws.Read(ctx)
		if e != nil {
			return
		}
		if typ != websocket.MessageBinary {
			return
		}
		plain, e := rx.Decrypt(nil, nil, msg)
		if e != nil {
			return
		}
		raw, e := reass.push(plain, time.Now())
		if e != nil {
			return
		}
		if raw == nil {
			continue
		}
		frame, requestBody, pathName, e := mockRequest(raw)
		if e != nil {
			return
		}
		m.record(pathName)
		if strings.HasPrefix(pathName, "/fs/") {
			var request map[string]any
			_ = json.Unmarshal(requestBody, &request)
			p, _ := request["path"].(string)
			switch pathName {
			case "/fs/write":
				encoded, _ := request["data_base64"].(string)
				chunk, _ := base64.StdEncoding.DecodeString(encoded)
				m.mu.Lock()
				if overwrite, _ := request["overwrite"].(bool); overwrite {
					m.files[p] = nil
				}
				m.files[p] = append(m.files[p], chunk...)
				size := len(m.files[p])
				m.mu.Unlock()
				sendResponse(ctx, ws, tx, frame, 200, []byte(fmt.Sprintf(`{"kind":"file","size":%d}`, size)), true)
			case "/fs/stat":
				m.mu.Lock()
				data, ok := m.files[p]
				size := len(data)
				m.mu.Unlock()
				if !ok {
					sendResponse(ctx, ws, tx, frame, 404, []byte(`{"error":"missing"}`), true)
				} else {
					sendResponse(ctx, ws, tx, frame, 200, []byte(fmt.Sprintf(`{"kind":"file","size":%d}`, size)), true)
				}
			case "/fs/rename":
				src, _ := request["source"].(string)
				dst, _ := request["destination"].(string)
				m.mu.Lock()
				m.files[dst] = m.files[src]
				delete(m.files, src)
				m.mu.Unlock()
				sendResponse(ctx, ws, tx, frame, 200, []byte(`{"ok":true}`), true)
			case "/fs/delete":
				m.mu.Lock()
				delete(m.files, p)
				m.mu.Unlock()
				sendResponse(ctx, ws, tx, frame, 200, []byte(`{"ok":true}`), true)
			case "/fs/read":
				offset := int(number(request["offset"]))
				length := int(number(request["len"]))
				m.mu.Lock()
				data := append([]byte(nil), m.files[p]...)
				m.mu.Unlock()
				if offset > len(data) {
					offset = len(data)
				}
				end := offset + length
				if end > len(data) {
					end = len(data)
				}
				chunk := data[offset:end]
				body := fmt.Sprintf(`{"data_base64":%q,"len":%d,"eof":%t}`, base64.StdEncoding.EncodeToString(chunk), len(chunk), end == len(data))
				sendResponse(ctx, ws, tx, frame, 200, []byte(body), true)
			}
			continue
		}
		if pathName == "/chat/subscribe" {
			subID = frame
			sendResponse(ctx, ws, tx, frame, 200, []byte(`{"status":"ok","payload":{"capabilities":[]}}`+"\n"), false)
			for _, event := range m.subscribeEvents {
				encoded, _ := json.Marshal(event)
				sendBodyChunk(ctx, ws, tx, subID, append(encoded, '\n'), false)
			}
			continue
		}
		if pathName == "/chat/stream" {
			sendResponse(ctx, ws, tx, frame, 200, []byte(`{}`), true)
			node := requestNode(requestBody)
			if m.delayEvents {
				time.Sleep(150 * time.Millisecond)
			}
			if m.streamEvents != nil {
				events := m.streamEvents(node)
				for i, event := range events {
					encoded, _ := json.Marshal(event)
					sendBodyChunk(ctx, ws, tx, subID, append(encoded, '\n'), i == len(events)-1)
				}
				continue
			}
			event := func(name, text string) []byte {
				return []byte(`{"event":"` + name + `","payload":{"chat_context":{"device_id":"` + node + `"},"text":"` + text + `"}}`)
			}
			sendBodyChunk(ctx, ws, tx, subID, event("delta.text_append", "hello "), false)
			sendBodyChunk(ctx, ws, tx, subID, append(event("delta.text_append", "world"), '\n'), false)
			done := []byte(`{"event":"delta.message_done","payload":{"chat_context":{"device_id":"` + node + `"},"status":"completed"}}`)
			sendBodyChunk(ctx, ws, tx, subID, append(done, '\n'), true)
			continue
		}
		if strings.HasPrefix(pathName, "/api/ping") {
			sendResponse(ctx, ws, tx, frame, 200, []byte(`{"ok":true}`), true)
			continue
		}
		sendResponse(ctx, ws, tx, frame, 200, []byte(`{}`), true)
	}
}

var onceMu sync.Mutex
var onceChans = map[chan struct{}]bool{}

func closeOnce(ch chan struct{}) {
	onceMu.Lock()
	defer onceMu.Unlock()
	if onceChans[ch] {
		return
	}
	onceChans[ch] = true
	close(ch)
}
func requestNode(body []byte) string {
	var v map[string]any
	_ = json.Unmarshal(body, &v)
	s, _ := v["node_id"].(string)
	return s
}
func mockRequest(raw []byte) (uint64, []byte, string, error) {
	service, err := fieldBytes(raw, 2)
	if err != nil {
		return 0, nil, "", err
	}
	fs, err := parseFields(service)
	if err != nil {
		return 0, nil, "", err
	}
	var id uint64
	var req []byte
	for _, f := range fs {
		if f.n == 1 {
			id = f.v
		}
		if f.n == 2 {
			req = f.b
		}
	}
	rf, err := parseFields(req)
	if err != nil {
		return 0, nil, "", err
	}
	var body []byte
	var p string
	for _, f := range rf {
		if f.n == 2 {
			p = string(f.b)
		}
		if f.n == 4 {
			body = f.b
		}
	}
	return id, body, p, nil
}
func sendResponse(ctx context.Context, ws *websocket.Conn, tx *noise.CipherState, id uint64, status int, body []byte, end bool) {
	resp := putVar(nil, 1, uint64(status))
	resp = putBytes(resp, 3, body)
	if end {
		resp = putVar(resp, 4, 1)
	}
	frame := putVar(nil, 1, id)
	frame = putBytes(frame, 3, resp)
	sendLogical(ctx, ws, tx, putBytes(nil, 1, frame))
}
func sendBodyChunk(ctx context.Context, ws *websocket.Conn, tx *noise.CipherState, id uint64, body []byte, end bool) {
	chunk := putBytes(nil, 1, body)
	if end {
		chunk = putVar(chunk, 2, 1)
	}
	frame := putVar(nil, 1, id)
	frame = putBytes(frame, 4, chunk)
	sendLogical(ctx, ws, tx, putBytes(nil, 1, frame))
}
func sendLogical(ctx context.Context, ws *websocket.Conn, tx *noise.CipherState, raw []byte) {
	frames, _ := pack(raw)
	for _, f := range frames {
		enc, e := tx.Encrypt(nil, nil, f)
		if e != nil {
			return
		}
		_ = ws.Write(ctx, websocket.MessageBinary, enc)
	}
}
func TestReassemblerOutOfOrderAndBounds(t *testing.T) {
	raw := []byte(strings.Repeat("x", maxPlaintextFrame*2+13))
	frames, err := pack(raw)
	if err != nil {
		t.Fatal(err)
	}
	dup := reassembler{}
	if _, err = dup.push(frames[0], time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = dup.push(frames[0], time.Now()); err == nil {
		t.Fatal("duplicate fragment was accepted")
	}
	r := reassembler{}
	var got []byte
	for i := len(frames) - 1; i >= 0; i-- {
		got, err = r.push(frames[i], time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	if string(got) != string(raw) {
		t.Fatalf("reassembled payload mismatch: got %d want %d", len(got), len(raw))
	}
}
func TestMockHandshakeGenerateUploadDownload(t *testing.T) {
	m := newMockHatch(t)
	defer m.close()
	c := New(Config{BaseURL: m.server.URL, WSURL: m.wsURL, Cookies: map[string]string{"hatch_sess": "placeholder"}})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if got := c.Cookies()["hatch_vml"]; got != "renewed" {
		t.Fatalf("cookie renewal missing: %q", got)
	}
	events := []Event{}
	result, err := c.Generate(ctx, GenerateRequest{NodeID: "node-test", Prompt: "hello", Messages: []Message{{Role: "user", Content: "hello"}}}, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello world" {
		t.Fatalf("text=%q", result.Text)
	}
	if len(events) < 3 {
		t.Fatalf("events=%d", len(events))
	}
	att, err := c.Upload(ctx, []byte("file-content"), "text/plain", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	t.Logf("paths=%v files=%v", m.paths, m.files)
	m.mu.Unlock()
	got, err := c.Download(ctx, att)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "file-content" {
		t.Fatalf("download=%q", got)
	}
}
func TestGenerateCancellation(t *testing.T) {
	m := newMockHatch(t)
	m.delayEvents = true
	defer m.close()
	c := New(Config{BaseURL: m.server.URL, WSURL: m.wsURL})
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	_, err := c.Generate(ctx, GenerateRequest{Prompt: "hello"}, func(e Event) error {
		if e.Type == "status" {
			closeOnce(started)
			cancel()
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	var he *Error
	if !errors.As(err, &he) || he.Kind != "network" {
		t.Fatalf("unexpected error %T %v", err, err)
	}
}
