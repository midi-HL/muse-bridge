package core

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/midi-HL/muse-bridge/internal/media"
	"github.com/midi-HL/muse-bridge/internal/secure"
)

var sha256Hex = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

func (s *Service) storeDownloadedModel(displayName string, data []byte, expected string) (map[string]any, error) {
	if len(data) > 128<<20 {
		return nil, media.ErrTooLarge
	}
	digest := media.SHA256(data)
	if expected != "" && (!sha256Hex.MatchString(expected) || !strings.EqualFold(expected, digest)) {
		return nil, errors.New("model SHA-256 verification failed")
	}
	dir := filepath.Join(s.dataDir, "models")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("cannot create model directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("cannot open model directory")
	}
	defer root.Close()
	name := secure.ID() + "-" + generatedDisplayName(displayName)
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("cannot create model file")
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = root.Remove(name)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return nil, errors.New("cannot save model file")
	}
	if err = file.Close(); err != nil {
		return nil, errors.New("cannot close model file")
	}
	keep = true
	// Return a data-directory-relative path usable for deployment configuration,
	// never the host's absolute user profile or workspace location.
	return map[string]any{"filename": generatedDisplayName(displayName), "path": "models/" + name, "size": len(data), "sha256": digest}, nil
}
