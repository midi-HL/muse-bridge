package core

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

// ErrDailyLimit remains compatible with the shared HTTP/store rate-limit error.
var ErrDailyLimit = errors.Join(store.ErrLimit, errors.New("daily chat request limit reached"))

func chatDailySubject(req ChatRequest) (string, error) {
	platform := req.Platform
	owner := req.UserID
	if platform == "" || owner == "" {
		return "", errors.New("chat caller identity is required")
	}
	if platform == "telegram" {
		if req.RetryUserID != "" && req.RetryUserID != "0" {
			owner = "sender:" + req.RetryUserID
		} else {
			owner = "chat:" + req.UserID
		}
	}
	return secure.Digest(platform + "\x00" + owner), nil
}

func chatDailyPeriod(settings map[string]any, now time.Time) (string, error) {
	zone, _ := settings["general.timezone"].(string)
	periodZone := zone
	if periodZone == "" {
		periodZone = "UTC"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return "", errors.New("configured chat quota time zone is invalid")
	}
	// Keep each zone's daily window distinct when the administrator changes it.
	return periodZone + ":" + now.In(location).Format("2006-01-02"), nil
}

// ValidateAttachmentMetadata performs bounded local preflight before transport
// quota accounting. It does not retain or decode the payload bytes.
func ValidateAttachmentMetadata(uploads []Upload) error {
	if len(uploads) > 10 {
		return errors.New("too many attachments")
	}
	for _, upload := range uploads {
		if len(upload.DataBase64) > base64.StdEncoding.EncodedLen(64<<20) {
			return errors.New("attachment exceeds 64 MiB")
		}
		if len(upload.Filename) > 1024 || len(upload.MIME) > 256 {
			return errors.New("attachment metadata exceeds protocol limits")
		}
		if _, err := base64.StdEncoding.DecodeString(upload.DataBase64); err != nil {
			return errors.New("invalid attachment encoding")
		}
	}
	return nil
}

func PrepareChatRequest(req *ChatRequest, persona string) error {
	if err := ValidateAttachmentMetadata(req.Attachments); err != nil {
		return err
	}
	if err := hatch.ValidateGenerateRequest(hatch.GenerateRequest{Model: req.Model, Prompt: req.Prompt, Persona: persona, Messages: req.Messages, Attachments: make([]hatch.Attachment, len(req.Attachments))}); err != nil {
		return err
	}
	cacheValid := len(req.validatedUploads) == len(req.Attachments)
	if cacheValid {
		for i, upload := range req.Attachments {
			cached := req.validatedUploads[i]
			if cached.Encoded != upload.DataBase64 || cached.MIME != upload.MIME || cached.Filename != upload.Filename || len(cached.Data) > 64<<20 {
				cacheValid = false
				break
			}
		}
	}
	if cacheValid {
		return nil
	}
	validated := make([]validatedChatUpload, len(req.Attachments))
	for i, upload := range req.Attachments {
		data, err := base64.StdEncoding.DecodeString(upload.DataBase64)
		if err != nil {
			req.validatedUploads = nil
			return errors.New("invalid attachment encoding")
		}
		if len(data) > 64<<20 {
			req.validatedUploads = nil
			return errors.New("attachment exceeds 64 MiB")
		}
		validated[i] = validatedChatUpload{Encoded: upload.DataBase64, Data: data, MIME: upload.MIME, Filename: upload.Filename}
	}
	req.validatedUploads = validated
	return nil
}

func (s *Service) chargeDailyChat(ctx context.Context, req ChatRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	settings := s.Settings()
	subject, err := chatDailySubject(req)
	if err != nil {
		return err
	}
	period, err := chatDailyPeriod(settings, time.Now())
	if err != nil {
		return err
	}
	limit := settingInt(settings, "limits.daily_per_user", 100)
	if _, err := s.db.Increment(ctx, "chat_day", subject, period, limit); err != nil {
		if errors.Is(err, store.ErrLimit) {
			return ErrDailyLimit
		}
		return err
	}
	return nil
}

// Keep quota errors stable and secret-free for Telegram's trusted adapter.
func ChatLimitMessage(err error) string {
	switch {
	case errors.Is(err, ErrDailyLimit):
		return "今日请求次数已达上限，请在配置时区的次日重试或联系管理员。"
	case errors.Is(err, ErrQueueFull):
		return "当前请求队列已满，请稍后重试。"
	default:
		return ""
	}
}
