package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

var modelIDs = []string{"muse-spark", "muse-image", "muse-video", "gpt-4o-mini"}

func validModel(model string) bool {
	for _, m := range modelIDs {
		if m == model {
			return true
		}
	}
	return false
}
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	if s.keys == nil {
		failure(w, 503, "unavailable", "Service is not ready")
		return
	}
	if _, ok := s.apiKey(w, r, false); !ok {
		return
	}
	list := []map[string]any{}
	for _, id := range modelIDs {
		list = append(list, map[string]any{"id": id, "object": "model", "created": 0, "owned_by": "muse"})
	}
	jsonResponse(w, 200, map[string]any{"object": "list", "data": list})
}
func validateChat(w http.ResponseWriter, b *core.ChatRequest) bool {
	if b.Model == "" {
		b.Model = "muse-spark"
	}
	if !validModel(b.Model) {
		failure(w, 400, "model_not_found", "Unknown model")
		return false
	}
	if len(b.SessionID) > 128 || len(b.Prompt) > 256<<10 || len(b.Messages) > 500 || len(b.Attachments) > 10 {
		failure(w, 400, "invalid_request", "Chat input exceeds limits")
		return false
	}
	if strings.TrimSpace(b.Prompt) == "" && len(b.Messages) == 0 && len(b.Attachments) == 0 {
		failure(w, 400, "invalid_request", "A message or attachment is required")
		return false
	}
	for _, m := range b.Messages {
		if (m.Role != "system" && m.Role != "user" && m.Role != "assistant") || len(m.Content) > 256<<10 {
			failure(w, 400, "invalid_request", "Unsupported message role or size")
			return false
		}
	}
	return true
}
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !s.authenticated(w, r, true) {
		return
	}
	var b core.ChatRequest
	if !decode(w, r, &b, 64<<20) || !validateChat(w, &b) {
		return
	}
	b.Platform = "web"
	b.UserID = "admin"
	s.streamChat(w, r, b)
}
func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !s.authenticated(w, r, true) {
		return
	}
	var b struct {
		SessionID string `json:"session_id"`
	}
	if !decode(w, r, &b, 4096) {
		return
	}
	v, e := s.Action(r.Context(), "chat.stop", map[string]any{"session_id": b.SessionID, "id": b.SessionID})
	if e != nil {
		safeError(w, e)
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "data": v})
}
func (s *Server) simpleChat(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	key, ok := s.validateAPIKey(w, r)
	if !ok {
		return
	}
	var b core.ChatRequest
	if !decode(w, r, &b, 64<<20) || !validateChat(w, &b) {
		return
	}
	if err := core.ValidateAttachmentMetadata(b.Attachments); err != nil {
		failure(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.chargeAPIKey(w, r, key) {
		return
	}
	b.Platform = "api"
	b.UserID = key.ID
	b.SessionID = apiSession(key.ID, b.SessionID)
	if strings.HasSuffix(r.URL.Path, "/stream") {
		s.streamChat(w, r, b)
		return
	}
	result, e := s.backend.Chat(r.Context(), b, func(core.ChatEvent) error { return nil })
	if e != nil {
		safeError(w, e)
		return
	}
	jsonResponse(w, 200, result)
}

// Client supplied session ids are never allowed to select another key's session.
func apiSession(key, id string) string {
	if id == "" {
		id = secure.ID()
	}
	prefix := "api_" + secure.Digest(key)[:20] + "_"
	if strings.HasPrefix(id, prefix) {
		return id
	}
	return prefix + secure.Digest(key + ":" + id)[:40]
}
func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
}
func sse(w http.ResponseWriter, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	if err == nil {
		err = controller.Flush()
	}
	if clearErr := controller.SetWriteDeadline(time.Time{}); err == nil && !errors.Is(clearErr, http.ErrNotSupported) {
		err = clearErr
	}
	return err
}
func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, b core.ChatRequest) {
	if _, ok := w.(http.Flusher); !ok {
		failure(w, 500, "stream_unavailable", "Streaming unavailable")
		return
	}
	sseHeaders(w)
	var mu sync.Mutex
	result, e := s.backend.Chat(r.Context(), b, func(event core.ChatEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if event.Type == "done" {
			return nil
		}
		if event.Type == "text" {
			event.Type = "delta"
		}
		return sse(w, event)
	})
	mu.Lock()
	defer mu.Unlock()
	if e != nil {
		_, code, msg := errorInfo(e)
		_ = sse(w, map[string]any{"type": "error", "error": map[string]string{"code": code, "message": msg}, "text": msg})
		return
	}
	_ = sse(w, map[string]any{"type": "done", "session_id": result.SessionID, "node_id": result.NodeID, "text": result.Text, "attachments": result.Attachments})
}
func (s *Server) completions(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	key, ok := s.validateAPIKey(w, r)
	if !ok {
		return
	}
	var b struct {
		Model          string          `json:"model"`
		Messages       []hatch.Message `json:"messages"`
		Stream         bool            `json:"stream"`
		N              int             `json:"n"`
		Tools          json.RawMessage `json:"tools"`
		ToolChoice     json.RawMessage `json:"tool_choice"`
		ResponseFormat json.RawMessage `json:"response_format"`
		SessionID      string          `json:"session_id"`
		PersonaID      string          `json:"persona_id"`
	}
	if !decode(w, r, &b, 2<<20) {
		return
	}
	if len(b.Tools) > 0 && string(b.Tools) != "null" || len(b.ToolChoice) > 0 && string(b.ToolChoice) != "null" || len(b.ResponseFormat) > 0 && string(b.ResponseFormat) != "null" || b.N > 1 {
		failure(w, 400, "unsupported", "Tools, structured output and multiple choices are unsupported")
		return
	}
	req := core.ChatRequest{Model: b.Model, Messages: b.Messages, PersonaID: b.PersonaID, Platform: "api", UserID: key.ID, SessionID: apiSession(key.ID, b.SessionID)}
	if len(req.Messages) == 0 {
		failure(w, 400, "invalid_request", "messages is required")
		return
	}
	if !validateChat(w, &req) {
		return
	}
	if !s.chargeAPIKey(w, r, key) {
		return
	}
	id := "chatcmpl-" + secure.ID()
	created := time.Now().Unix()
	if !b.Stream {
		result, e := s.backend.Chat(r.Context(), req, func(core.ChatEvent) error { return nil })
		if e != nil {
			safeError(w, e)
			return
		}
		content := result.Text
		for _, f := range result.Attachments {
			content += "\n[" + f.Filename + "](/files/" + f.ID + ")"
		}
		jsonResponse(w, 200, map[string]any{"id": id, "object": "chat.completion", "created": created, "model": req.Model, "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		failure(w, 500, "stream_unavailable", "Streaming unavailable")
		return
	}
	sseHeaders(w)
	chunk := func(delta any, reason any) any {
		return map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}}
	}
	if sse(w, chunk(map[string]string{"role": "assistant"}, nil)) != nil {
		return
	}
	var mu sync.Mutex
	var emitted bool
	result, e := s.backend.Chat(r.Context(), req, func(event core.ChatEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if event.Type == "text" || event.Type == "delta" {
			emitted = true
			return sse(w, chunk(map[string]string{"content": event.Text}, nil))
		}
		return nil
	})
	mu.Lock()
	defer mu.Unlock()
	if e != nil {
		_, code, msg := errorInfo(e)
		_ = sse(w, map[string]any{"error": map[string]string{"code": code, "type": code, "message": msg}})
		return
	}
	if !emitted && result.Text != "" {
		if sse(w, chunk(map[string]string{"content": result.Text}, nil)) != nil {
			return
		}
	}
	for _, f := range result.Attachments {
		if sse(w, chunk(map[string]string{"content": "\n[" + f.Filename + "](/files/" + f.ID + ")"}, nil)) != nil {
			return
		}
	}
	if sse(w, chunk(map[string]string{}, "stop")) != nil {
		return
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	_ = http.NewResponseController(w).Flush()
}
