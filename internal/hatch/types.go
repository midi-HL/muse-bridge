// Package hatch implements the standard Muse Hatch VM's Noise XX transport.
// It contains no browser automation or account registration functionality.
package hatch

import (
	"context"
	"net/http"
)

// Config selects bootstrap and transport endpoints. Empty URLs use production
// endpoints. HTTPClient is copied; its transport is shared unless ProxyURL is set.
// Cookies and ProxyURL contain secrets and must never be logged.
type Config struct {
	Cookies    map[string]string
	ProxyURL   string
	BaseURL    string
	WSURL      string
	HTTPClient *http.Client
}

// GenerateRequest supplies a complete locally persisted conversation. NodeID is
// the upstream device/task correlation ID, not a proven persistent thread ID.
// Prompt is appended unless it equals the final user message. Persona is a
// best-effort textual instruction, not a replacement for Muse's system prompt.
type GenerateRequest struct {
	NodeID      string       `json:"node_id,omitempty"`
	Prompt      string       `json:"prompt,omitempty"`
	Model       string       `json:"model,omitempty"`
	Persona     string       `json:"persona,omitempty"`
	Messages    []Message    `json:"messages,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Event types are node, text, attachment, status and done. Text events are deltas.
type Event struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	NodeID     string      `json:"node_id,omitempty"`
	Attachment *Attachment `json:"attachment,omitempty"`
}
type GenerateResult struct {
	NodeID      string       `json:"node_id"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}
type Attachment struct {
	Path     string `json:"path"`
	MIME     string `json:"mime"`
	Filename string `json:"filename"`
}
type ProbeResult struct {
	Status  string `json:"status"`
	VMState string `json:"vm_state"`
	// Quota is nil when no evidenced pure-protocol quota route is available.
	Quota map[string]any `json:"quota"`
	// Cookies is for encrypted persistence only; excluded from API JSON responses.
	Cookies map[string]string `json:"-"`
}
type Thread struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type Task struct {
	ID     string `json:"id"`
	NodeID string `json:"node_id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Error exposes only fixed local diagnostics. Raw upstream text, underlying
// network errors, response bodies and credential-bearing URLs are never retained.
type Error struct {
	Kind    string `json:"kind"`
	Op      string `json:"op"`
	Message string `json:"message"`
	cause   error
}

func (e *Error) Error() string              { return "hatch: " + e.Op + ": " + e.Message }
func (e *Error) Unwrap() error              { return e.cause }
func fault(kind, op, message string) *Error { return &Error{Kind: kind, Op: op, Message: message} }
func contextError(op string, err error) error {
	if err == context.Canceled || err == context.DeadlineExceeded {
		return &Error{Kind: "network", Op: op, Message: "operation cancelled or timed out", cause: err}
	}
	return fault("network", op, "connection failed")
}
func statusError(op string, status int) error {
	switch status {
	case 401:
		return fault("auth", op, "authentication failed")
	case 429:
		return fault("rate_limit", op, "upstream rate limit exceeded")
	case 404, 405, 501:
		return fault("unsupported", op, "upstream does not support this operation")
	default:
		return fault("protocol", op, "upstream rejected the request")
	}
}
