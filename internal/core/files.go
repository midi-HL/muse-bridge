package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/midi-HL/muse-bridge/internal/secure"
)

// A file record may contain a new root-relative basename or a legacy path
// produced by materialize. Legacy paths must resolve lexically beneath files/.
// Actual access always uses os.Root, which also confines symlink traversal.
func (s *Service) fileStorageName(recorded string) (string, error) {
	if recorded == "" || strings.ContainsAny(recorded, "\x00\r\n") {
		return "", sql.ErrNoRows
	}
	normalized := strings.ReplaceAll(recorded, "\\", "/")
	for _, part := range strings.Split(normalized, "/") {
		if part == "." || part == ".." {
			return "", sql.ErrNoRows
		}
	}
	if !filepath.IsAbs(recorded) && !strings.ContainsAny(recorded, "/\\:") {
		if !filepath.IsLocal(recorded) {
			return "", sql.ErrNoRows
		}
		return recorded, nil
	}
	// Do not reinterpret a foreign-platform drive/UNC path as a relative name.
	if filepath.VolumeName(recorded) == "" && (strings.Contains(recorded, ":") || strings.HasPrefix(normalized, "//")) {
		return "", sql.ErrNoRows
	}
	root, err := filepath.Abs(filepath.Join(s.dataDir, "files"))
	if err != nil {
		return "", sql.ErrNoRows
	}
	candidate, err := filepath.Abs(recorded)
	if err != nil {
		return "", sql.ErrNoRows
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || !filepath.IsLocal(rel) || rel == "." || strings.ContainsAny(rel, "/\\:") {
		return "", sql.ErrNoRows
	}
	return rel, nil
}

// Imported legacy paths only identify a generated filename. They are never
// opened on the source host path. Accept the old files/<id>-<display> layout
// (including Windows backups restored on Linux) and rebase that basename.
func (s *Service) importedFileName(rec fileRecord, id string) (string, error) {
	if name, err := s.fileStorageName(rec.Path); err == nil {
		return name, nil
	}
	normalized := strings.ReplaceAll(rec.Path, "\\", "/")
	if strings.ContainsAny(normalized, "\x00\r\n") {
		return "", sql.ErrNoRows
	}
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		if part == ".." || part == "." {
			return "", sql.ErrNoRows
		}
	}
	name := path.Base(normalized)
	if path.Base(path.Dir(normalized)) != "files" || !strings.HasPrefix(name, id+"-") || name == id+"-" || strings.ContainsAny(name, ":") || !filepath.IsLocal(name) {
		return "", sql.ErrNoRows
	}
	return name, nil
}

func validFileID(id string) bool {
	// Historical secure.ID values can begin with '_' or '-'. HTTP routing
	// separately rejects separators and traversal; keep legacy IDs accessible.
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (s *Service) openRecordFile(rec fileRecord) (*os.File, error) {
	name, err := s.fileStorageName(rec.Path)
	if err != nil || s.fileRoot == nil {
		return nil, sql.ErrNoRows
	}
	// Generated files are regular files, never links or devices. Refuse them
	// before opening to avoid blocking on FIFOs; os.Root also confines races.
	entry, err := s.fileRoot.Lstat(name)
	if err != nil || !entry.Mode().IsRegular() {
		return nil, sql.ErrNoRows
	}
	f, err := s.fileRoot.Open(name)
	if err != nil {
		return nil, sql.ErrNoRows
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, sql.ErrNoRows
	}
	return f, nil
}

// OpenFile is the HTTP file boundary: authorization and confinement precede
// opening, and the caller receives the already-open handle (no path reopen).
// Empty platform denotes an authenticated administrator; legacy ownerless files
// remain admin-only rather than becoming readable by every API key.
func (s *Service) OpenFile(ctx context.Context, id, platform, userID string) (*os.File, File, error) {
	if !validFileID(id) {
		return nil, File{}, sql.ErrNoRows
	}
	var rec fileRecord
	if err := s.db.Get(ctx, "file", id, &rec); err != nil {
		return nil, File{}, sql.ErrNoRows
	}
	if rec.ID != "" && rec.ID != id {
		return nil, File{}, sql.ErrNoRows
	}
	if platform != "" && (userID == "" || rec.Platform != platform || rec.UserID != userID) {
		return nil, File{}, sql.ErrNoRows
	}
	f, err := s.openRecordFile(rec)
	if err != nil {
		return nil, File{}, err
	}
	rec.ID = id
	return f, rec.File, nil
}

// File is a metadata compatibility API. HTTP serving must use OpenFile so that
// the validated path cannot be exchanged for a symlink between check and open.
func (s *Service) File(ctx context.Context, id string) (string, File, error) {
	f, meta, err := s.OpenFile(ctx, id, "", "")
	if err != nil {
		return "", File{}, err
	}
	defer f.Close()
	path, err := filepath.Abs(f.Name())
	if err != nil {
		return "", File{}, sql.ErrNoRows
	}
	return path, meta, nil
}

func generatedDisplayName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if len(name) > 180 {
		name = name[:180]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	if name == "" {
		return "download.bin"
	}
	return name
}

func (s *Service) storeGeneratedFile(ctx context.Context, name, mime string, data []byte, platform, userID string) (File, error) {
	id := "f_" + secure.ID()
	// Only the opaque ID is used on disk, so reserved names, ADS suffixes and
	// hostile remote filenames cannot affect filesystem operations.
	filename := id + ".bin"
	f, err := s.fileRoot.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return File{}, errors.New("cannot create generated file")
	}
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = s.fileRoot.Remove(filename)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return File{}, errors.New("cannot write generated file")
	}
	if err = f.Close(); err != nil {
		return File{}, errors.New("cannot close generated file")
	}
	meta := File{ID: id, Filename: generatedDisplayName(name), MIME: mime}
	if err = s.db.Put(ctx, "file", id, fileRecord{File: meta, Path: filename, Platform: platform, UserID: userID}, nil); err != nil {
		return File{}, err
	}
	keep = true
	return meta, nil
}
