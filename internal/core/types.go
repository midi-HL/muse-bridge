package core

import "github.com/midi-HL/muse-bridge/internal/hatch"

type Upload struct {
	Filename   string `json:"filename"`
	MIME       string `json:"mime"`
	DataBase64 string `json:"data_base64"`
}

type ChatRequest struct {
	SessionID      string          `json:"session_id"`
	Platform       string          `json:"platform"`
	UserID         string          `json:"user_id"`
	Prompt         string          `json:"prompt"`
	Model          string          `json:"model"`
	PersonaID      string          `json:"persona_id"`
	Retry          bool            `json:"-"`
	RetryUserID    string          `json:"-"`
	RetryAttemptID string          `json:"-"`
	Attachments    []Upload        `json:"attachments,omitempty"`
	Messages       []hatch.Message `json:"messages,omitempty"`
}

type File struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	MIME     string `json:"mime"`
}

type ChatEvent struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Attachment *File  `json:"attachment,omitempty"`
}

type ChatResult struct {
	SessionID   string `json:"session_id"`
	NodeID      string `json:"node_id"`
	Text        string `json:"text"`
	Attachments []File `json:"attachments,omitempty"`
}
