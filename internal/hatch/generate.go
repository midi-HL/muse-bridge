package hatch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path"
	"strings"
	"time"
)

const maxEventLine = 1 << 20

// Generate forwards real subscription deltas immediately. It does not buffer the
// full response before invoking emit. emit must return promptly and must not call
// Generate recursively; returning an error stops delivery. Cancel context to stop
// waiting locally. Neither cancellation nor stream reset proves remote compute stopped.
func effectivePrompt(req GenerateRequest, model string) string {
	prompt := buildPrompt(req)
	switch model {
	case "muse-image":
		if len(req.Attachments) > 0 {
			return "基于我本次上传附带的参考素材进行生图/编辑，输出一张图片：" + prompt
		}
		return "全新文生图创作（当前未提供任何参考图，请勿查找历史相册或向用户索要原图，直接根据文字描述从零绘制生成一张全新图片）：" + prompt
	case "muse-video":
		if len(req.Attachments) > 0 {
			return "基于我本次上传的参考素材生成视频（只使用本次上传的这些附件）：" + prompt
		}
		return "全新文生视频创作（纯文本全新生成）：生成一个视频（时长严格为 6 秒）：" + prompt
	default:
		return prompt
	}
}

func ValidateGenerateRequest(req GenerateRequest) error {
	model := req.Model
	if model == "" {
		model = "muse-spark"
	}
	if model != "muse-spark" && model != "gpt-4o-mini" && model != "muse-image" && model != "muse-video" {
		return fault("unsupported", "generate", "model is not supported")
	}
	if len(req.Attachments) > 10 {
		return fault("unsupported", "generate", "at most ten reference attachments are supported")
	}
	if len(effectivePrompt(req, model)) > maxEventLine {
		return fault("protocol", "generate", "prompt exceeds size limit")
	}
	return nil
}

func (c *Client) Generate(ctx context.Context, req GenerateRequest, emit func(Event) error) (result GenerateResult, err error) {
	ctx, cancel := c.operation(ctx, 10*time.Minute)
	defer cancel()
	if err = acquire(ctx, c.generateGate); err != nil {
		return
	}
	defer func() { c.generateGate <- struct{}{} }()
	model := req.Model
	if model == "" {
		model = "muse-spark"
	}
	if model != "muse-spark" && model != "gpt-4o-mini" && model != "muse-image" && model != "muse-video" {
		err = fault("unsupported", "generate", "model is not supported")
		return
	}
	if len(req.Attachments) > 10 {
		err = fault("unsupported", "generate", "at most ten reference attachments are supported")
		return
	}
	if err = ValidateGenerateRequest(req); err != nil {
		return
	}
	prompt := effectivePrompt(req, model)
	if req.NodeID == "" {
		req.NodeID = newID()
	}
	if len(req.NodeID) > 256 {
		err = fault("protocol", "generate", "node identifier exceeds limit")
		return
	}
	result.NodeID = req.NodeID
	send := func(e Event) error {
		e.NodeID = result.NodeID
		if emit != nil {
			if emit(e) != nil {
				return fault("protocol", "callback", "event consumer stopped delivery")
			}
		}
		return nil
	}
	cn, e := c.current(ctx)
	if e != nil {
		err = e
		return
	}
	id, s, e := cn.open(ctx, "POST", "/chat/subscribe", map[string]any{"after_stream_seq": 0, "after_chat_event_seq": 0, "capabilities": []string{}})
	if e != nil {
		err = e
		return
	}
	defer cn.release(id, true)
	sr := &subscriptionReader{cn: cn, s: s}
	ackCtx, ackCancel := context.WithTimeout(ctx, 30*time.Second)
	ack, e := sr.next(ackCtx)
	ackCancel()
	if e != nil {
		err = e
		return
	}
	if text(ack, "status") != "ok" {
		err = fault("protocol", "subscribe", "subscription was not accepted")
		return
	}
	caps := map[string]bool{}
	if values, ok := object(ack["payload"])["capabilities"].([]any); ok {
		for _, v := range values {
			if str, ok := v.(string); ok {
				caps[str] = true
			}
		}
	}
	items := make([]map[string]any, 0, len(req.Attachments)+1)
	for _, a := range req.Attachments {
		// Match protocol_engine.py's inline/file_ref thresholds. Upload returns a
		// stable reference; small/image payloads are reread through the same transport.
		data, e := c.Download(ctx, a)
		if e != nil {
			err = e
			return
		}
		useRef := !strings.HasPrefix(a.MIME, "image/") && len(data) >= 10<<20
		if strings.HasPrefix(a.MIME, "video/") && !caps["video_file_ref_ingest"] {
			useRef = len(data) >= 60<<20
		}
		if useRef {
			remote, e := sandboxPath(a.Path)
			if e != nil {
				err = e
				return
			}
			items = append(items, map[string]any{"type": "file_ref", "mime_type": a.MIME, "path": remote, "filename": a.Filename})
		} else {
			kind := "file"
			if strings.HasPrefix(a.MIME, "image/") {
				kind = "image"
			}
			items = append(items, map[string]any{"type": kind, "mime_type": a.MIME, "data_base64": base64.StdEncoding.EncodeToString(data), "filename": a.Filename})
		}
	}
	items = append(items, map[string]any{"type": "text", "text": prompt})
	if err = send(Event{Type: "node"}); err != nil {
		return
	}
	submitCtx, submitCancel := context.WithTimeout(ctx, 60*time.Second)
	_, err = cn.request(submitCtx, "POST", "/chat/stream", map[string]any{"items": items, "node_id": req.NodeID, "capabilities": map[string]any{}})
	submitCancel()
	if err != nil {
		return
	}
	if err = send(Event{Type: "status", Text: "generating"}); err != nil {
		return
	}
	seen := make(map[string]bool)
	sequenceOrder := make([]string, 0, 4096)
	seenMedia := make(map[string]bool)
	var output strings.Builder
	var done map[string]any
	var doneAt time.Time
	mediaMode := model == "muse-image" || model == "muse-video"
	for {
		evCtx := ctx
		var evCancel context.CancelFunc
		if !doneAt.IsZero() && mediaMode {
			evCtx, evCancel = context.WithDeadline(ctx, doneAt.Add(12*time.Second))
		}
		event, e := sr.next(evCtx)
		if evCancel != nil {
			evCancel()
		}
		if e != nil {
			err = e
			if !doneAt.IsZero() && ctx.Err() == nil {
				err = fault("protocol", "generate", "generation ended without the requested media")
			}
			return
		}
		if seq, ok := event["seq"]; ok {
			b, _ := json.Marshal(seq)
			key := string(b)
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(sequenceOrder) == cap(sequenceOrder) {
				delete(seen, sequenceOrder[0])
				copy(sequenceOrder, sequenceOrder[1:])
				sequenceOrder = sequenceOrder[:len(sequenceOrder)-1]
			}
			sequenceOrder = append(sequenceOrder, key)
		}
		name := text(event, "event")
		if name == "" {
			name = text(event, "type")
		}
		payload := object(event["payload"])
		if payload == nil || name == "res" {
			continue
		}
		device := text(object(payload["chat_context"]), "device_id")
		if device != "" && device != req.NodeID {
			continue
		}
		ours := device == req.NodeID
		switch name {
		case "delta.text_append":
			if !ours {
				continue
			}
			delta := text(payload, "text")
			if output.Len()+len(delta) > maxResponseBytes {
				err = fault("protocol", "generate", "generated text exceeds limit")
				return
			}
			output.WriteString(delta)
			result.Text = output.String()
			if delta != "" {
				if err = send(Event{Type: "text", Text: delta}); err != nil {
					return
				}
			}
		case "delta.presentation", "delta.message_done", "delta.tool_done", "delta.placeholder":
			resources := mediaCandidates(payload, 0)
			for _, a := range resources {
				if seenMedia[a.Path] {
					continue
				}
				if len(seenMedia) >= 128 {
					err = fault("protocol", "generate", "too many generated attachments")
					return
				}
				seenMedia[a.Path] = true
				result.Attachments = append(result.Attachments, a)
				attachment := a
				if err = send(Event{Type: "attachment", Attachment: &attachment}); err != nil {
					return
				}
			}
			if name == "delta.message_done" && (ours || (mediaMode && len(result.Attachments) > 0)) {
				status := text(payload, "status")
				if ours && status != "" && status != "completed" {
					err = fault("protocol", "generate", "upstream generation did not complete")
					return
				}
				done = payload
				if doneAt.IsZero() {
					doneAt = time.Now()
				}
			}
		case "delta.message_removed":
			if ours {
				err = fault("protocol", "generate", "upstream removed the generation")
				return
			}
		}
		matched := false
		for _, a := range result.Attachments {
			video := strings.HasPrefix(a.MIME, "video/") || strings.HasSuffix(a.Path, ".mp4") || strings.HasSuffix(a.Path, ".webm") || strings.HasSuffix(a.Path, ".mov")
			if model == "muse-video" && video || model == "muse-image" && !video {
				matched = true
			}
		}
		if (done != nil && !mediaMode) || (mediaMode && matched && (name == "delta.presentation" || done != nil)) {
			if result.Text == "" && done != nil {
				fallback := transcriptText(done)
				if len(fallback) > maxResponseBytes {
					err = fault("protocol", "generate", "transcript exceeds limit")
					return
				}
				result.Text = fallback
				if fallback != "" {
					if err = send(Event{Type: "text", Text: fallback}); err != nil {
						return
					}
				}
			}
			err = send(Event{Type: "done"})
			return
		}
	}
}
func buildPrompt(req GenerateRequest) string {
	parts := make([]string, 0, len(req.Messages)+2)
	if req.Persona != "" {
		parts = append(parts, "system: "+req.Persona)
	}
	for _, m := range req.Messages {
		if m.Content == "" {
			continue
		}
		if m.Role == "" || m.Role == "user" {
			parts = append(parts, m.Content)
		} else {
			parts = append(parts, m.Role+": "+m.Content)
		}
	}
	duplicate := false
	if len(req.Messages) > 0 {
		m := req.Messages[len(req.Messages)-1]
		duplicate = (m.Role == "user" || m.Role == "") && strings.TrimSpace(m.Content) == strings.TrimSpace(req.Prompt)
	}
	if req.Prompt != "" && !duplicate {
		parts = append(parts, req.Prompt)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

type subscriptionReader struct {
	cn      *connection
	s       *stream
	pending []byte
	ended   bool
}

func (r *subscriptionReader) next(ctx context.Context) (map[string]any, error) {
	for {
		// The upstream may concatenate JSON objects in one logical stream and
		// only terminate a later event with a newline. Decode one complete object
		// first, retaining any bytes that follow it.
		if len(bytes.TrimSpace(r.pending)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(r.pending))
			var ev map[string]any
			if dec.Decode(&ev) == nil && ev != nil && dec.InputOffset() > 0 {
				r.pending = r.pending[dec.InputOffset():]
				return ev, nil
			}
		}
		if i := bytes.IndexByte(r.pending, '\n'); i >= 0 {
			line := r.pending[:i]
			r.pending = r.pending[i+1:]
			if len(line) > maxEventLine {
				return nil, fault("protocol", "subscribe", "event line exceeds limit")
			}
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var ev map[string]any
			if json.Unmarshal(line, &ev) != nil || ev == nil {
				return nil, fault("protocol", "subscribe", "invalid subscription JSON")
			}
			return ev, nil
		}
		if len(r.pending) > maxEventLine {
			return nil, fault("protocol", "subscribe", "event line exceeds limit")
		}
		// Some Hatch responses terminate the JSON object at the logical stream
		// boundary instead of appending a newline. Decode one complete object
		// while retaining any following bytes for the next event.
		if len(r.pending) > 0 && !bytes.Contains(r.pending, []byte{'\n'}) {
			dec := json.NewDecoder(bytes.NewReader(r.pending))
			var ev map[string]any
			if dec.Decode(&ev) == nil && ev != nil && dec.InputOffset() > 0 {
				r.pending = r.pending[dec.InputOffset():]
				return ev, nil
			}
		}
		if r.ended {
			return nil, fault("network", "subscribe", "subscription ended before generation completed")
		}
		status, data, end, err := r.s.take()
		if err != nil {
			return nil, err
		}
		if status != 0 && (status < 200 || status >= 300) {
			return nil, statusError("subscribe", status)
		}
		if len(r.pending)+len(data) > maxResponseBytes {
			return nil, fault("protocol", "subscribe", "subscription buffer exceeds limit")
		}
		r.pending = append(r.pending, data...)
		r.ended = end
		if len(data) > 0 || end {
			continue
		}
		if err = r.cn.wait(ctx, r.s); err != nil {
			return nil, err
		}
	}
}
func transcriptText(done map[string]any) string {
	var out strings.Builder
	if messages, ok := object(done["transcript"])["messages"].([]any); ok {
		for _, m := range messages {
			if content, ok := object(m)["content"].([]any); ok {
				for _, p := range content {
					v := object(p)
					if text(v, "type") == "text" {
						out.WriteString(text(v, "text"))
					}
				}
			}
		}
	}
	return out.String()
}
func mediaCandidates(value any, depth int) []Attachment {
	if depth > 32 {
		return nil
	}
	var out []Attachment
	switch v := value.(type) {
	case map[string]any:
		if metadata, ok := v["metadata"].(string); ok && len(metadata) <= maxEventLine {
			var parsed map[string]any
			if json.Unmarshal([]byte(metadata), &parsed) == nil {
				out = append(out, mediaCandidates(parsed, depth+1)...)
			}
		}
		remote := ""
		for _, key := range []string{"path", "uri", "image_path", "video_path"} {
			if remote = text(v, key); remote != "" {
				break
			}
		}
		if normalized, err := sandboxPath(remote); err == nil && !strings.HasPrefix(normalized, "workspace/user/") {
			mime := text(v, "mime")
			if mime == "" {
				mime = text(v, "mime_type")
			}
			filename := text(v, "filename")
			if filename == "" {
				filename = path.Base(normalized)
			}
			out = append(out, Attachment{Path: "sandbox://" + normalized, MIME: mime, Filename: filename})
		}
		for key, child := range v {
			if key != "request" && key != "data_base64" {
				out = append(out, mediaCandidates(child, depth+1)...)
			}
		}
	case []any:
		for _, child := range v {
			out = append(out, mediaCandidates(child, depth+1)...)
		}
	}
	return out
}
