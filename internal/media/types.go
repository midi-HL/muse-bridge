// Package media provides bounded, request-scoped media processing without CGO.
package media

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotConfigured = errors.New("media: service is not configured")
	ErrTooLarge      = errors.New("media: size limit exceeded")
	ErrUnsupported   = errors.New("media: unsupported media or operation")
	ErrUnsafeURL     = errors.New("media: HTTPS public destination required")
)

type Attachment struct {
	Kind     string  `json:"kind"` // image, document, voice, audio, video, sticker
	Name     string  `json:"name"`
	MIME     string  `json:"mime"`
	Data     []byte  `json:"-"`
	Duration float64 `json:"duration,omitempty"` // seconds, if supplied by trusted metadata
}
type Prepared struct {
	Text        string
	Attachments []Attachment
}
type Transcript struct {
	Text     string        `json:"text"`
	Language string        `json:"language,omitempty"`
	Elapsed  time.Duration `json:"elapsed"`
	Duration float64       `json:"duration"`
	RTF      float64       `json:"rtf"` // zero when duration is unknown
}
type Preparer interface {
	Prepare(context.Context, Attachment) (Prepared, error)
}
