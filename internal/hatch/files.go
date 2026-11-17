package hatch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"path"
	"regexp"
	"strings"
	"time"
)

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sandboxPath(value string) (string, error) {
	p := strings.TrimPrefix(value, "sandbox://")
	if !strings.HasPrefix(p, "workspace/") || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) || strings.Contains(p, "?") || strings.Contains(p, "#") || len(p) > 4096 || path.Clean(p) != p {
		return "", fault("protocol", "file", "invalid sandbox resource path")
	}
	return p, nil
}
func decodeObject(raw []byte) (map[string]any, error) {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, fault("protocol", "decode", "invalid JSON response")
	}
	if r := object(value["result"]); r != nil {
		return r, nil
	}
	return value, nil
}

// Upload writes to a random staging file, verifies the remote size, and renames
// it atomically according to the reference. A failed upload attempts cleanup with
// its own short context; cleanup cannot guarantee deletion after a disconnect.
func (c *Client) Upload(ctx context.Context, data []byte, mimeType, filename string) (attachment Attachment, err error) {
	if len(data) > maxFileBytes {
		return attachment, fault("unsupported", "upload", "file exceeds 64 MiB limit")
	}
	if len(filename) > 1024 || len(mimeType) > 256 {
		return attachment, fault("protocol", "upload", "file metadata exceeds limit")
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	ctx, cancel := c.operation(ctx, 5*time.Minute)
	defer cancel()
	safe := strings.Trim(unsafeFilename.ReplaceAllString(filename, "_"), "._")
	if safe == "" {
		safe = "reference.bin"
	}
	if len(safe) > 180 {
		safe = safe[len(safe)-180:]
	}
	ext := strings.ToLower(path.Ext(safe))
	stem := strings.TrimSuffix(safe, path.Ext(safe))
	if ext == "" {
		ext = ".bin"
		if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
			ext = exts[0]
		}
	}
	final := "workspace/user/files/" + stem + "_0_" + strings.ReplaceAll(newID(), "-", "")[:8] + ext
	staging := "workspace/user/files/.upload-" + newID() + ".part"
	defer func() {
		if err != nil {
			cleanup, cancel := c.operation(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = c.request(cleanup, "POST", "/fs/delete", map[string]any{"path": staging, "recursive": false})
		}
	}()
	for offset := 0; ; {
		end := offset + 524288
		if end > len(data) {
			end = len(data)
		}
		_, err = c.request(ctx, "POST", "/fs/write", map[string]any{"path": staging, "data_base64": base64.StdEncoding.EncodeToString(data[offset:end]), "overwrite": offset == 0, "append": offset != 0, "create_parent": offset == 0})
		if err != nil {
			return
		}
		offset = end
		if offset == len(data) {
			break
		}
	}
	raw, e := c.request(ctx, "POST", "/fs/stat", map[string]any{"path": staging})
	if e != nil {
		err = e
		return
	}
	stat, e := decodeObject(raw)
	if e != nil {
		err = e
		return
	}
	size, ok := stat["size"].(float64)
	if text(stat, "kind") != "file" || !ok || size != float64(len(data)) {
		err = fault("protocol", "upload", "uploaded file size verification failed")
		return
	}
	_, err = c.request(ctx, "POST", "/fs/rename", map[string]any{"source": staging, "destination": final, "create_parent": true, "overwrite": true})
	if err != nil {
		return
	}
	return Attachment{Path: final, MIME: mimeType, Filename: filename}, nil
}
func (c *Client) Download(ctx context.Context, attachment Attachment) ([]byte, error) {
	remote, err := sandboxPath(attachment.Path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := c.operation(ctx, 5*time.Minute)
	defer cancel()
	var output []byte
	for {
		raw, err := c.request(ctx, "POST", "/fs/read", map[string]any{"path": remote, "offset": len(output), "len": 1048576})
		if err != nil {
			return nil, err
		}
		body, err := decodeObject(raw)
		if err != nil {
			return nil, err
		}
		encoded, ok := body["data_base64"].(string)
		if !ok {
			return nil, fault("protocol", "download", "missing file chunk")
		}
		chunk, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return nil, fault("protocol", "download", "invalid file encoding")
		}
		if len(chunk) > 1048576 {
			return nil, fault("protocol", "download", "file chunk exceeds requested length")
		}
		if length, exists := body["len"]; exists {
			n, ok := length.(float64)
			if !ok || n != float64(len(chunk)) {
				return nil, fault("protocol", "download", "file chunk length mismatch")
			}
		}
		if len(output)+len(chunk) > maxFileBytes {
			return nil, fault("unsupported", "download", "file exceeds 64 MiB limit")
		}
		output = append(output, chunk...)
		eof, ok := body["eof"].(bool)
		if !ok {
			return nil, fault("protocol", "download", "missing file EOF marker")
		}
		if eof {
			return output, nil
		}
		if len(chunk) == 0 {
			return nil, fault("protocol", "download", "empty non-EOF file chunk")
		}
	}
}
