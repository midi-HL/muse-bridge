package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/secure"
)

func TestLegacyRawURLFileIDsRemainAccessible(t *testing.T) {
	s, db := testService(t)
	for _, id := range []string{"_legacySafeID", "-legacySafeID"} {
		name := id + ".bin"
		path := filepath.Join(s.dataDir, "files", name)
		if err := os.WriteFile(path, []byte("legacy payload"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := db.Put(context.Background(), "file", id, fileRecord{File: File{ID: id, Filename: "legacy.txt"}, Path: name}, nil); err != nil {
			t.Fatal(err)
		}
		f, _, err := s.OpenFile(context.Background(), id, "", "")
		if err != nil {
			t.Fatalf("legacy id %q became unreadable: %v", id, err)
		}
		b, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || string(b) != "legacy payload" {
			t.Fatalf("legacy content id %q: %q %v", id, b, err)
		}
	}
}

func TestLegacyRawURLFileIDsSurviveBackupImport(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	ids := []string{"_legacySafeID", "-legacySafeID"}
	for _, id := range ids {
		name := id + ".bin"
		if err := os.WriteFile(filepath.Join(s.dataDir, "files", name), []byte("legacy payload"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := db.Put(ctx, "file", id, fileRecord{File: File{ID: id, Filename: "legacy.txt"}, Path: name}, nil); err != nil {
			t.Fatal(err)
		}
	}
	pass := "legacy-file-backup-passphrase"
	out, err := s.Action(ctx, "backup.export", map[string]any{"passphrase": pass})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testService(t)
	if _, err = target.Action(ctx, "backup.import", map[string]any{"passphrase": pass, "blob": out.(map[string]any)["blob"]}); err != nil {
		t.Fatalf("legacy file IDs rejected from backup: %v", err)
	}
	for _, id := range ids {
		name := id + ".bin"
		if err := os.WriteFile(filepath.Join(target.dataDir, "files", name), []byte("restored legacy payload"), 0600); err != nil {
			t.Fatal(err)
		}
		f, _, err := target.OpenFile(ctx, id, "", "")
		if err != nil {
			t.Fatalf("restored legacy id %q unavailable: %v", id, err)
		}
		b, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || string(b) != "restored legacy payload" {
			t.Fatalf("restored content %q %v", b, err)
		}
	}
}

func TestGeneratedFilesUseOpaqueNamesAndKeyOwnership(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	for _, remoteName := range []string{"../escaped.txt", `C:\\folder\\CON:stream`, "../\x00bad\r\nname", strings.Repeat("长", 200)} {
		meta, err := s.storeGeneratedFile(ctx, remoteName, "text/plain", []byte("generated payload"), "api", "owner-a")
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(meta.Filename, "/\\:\x00\r\n") || len(meta.Filename) > 180 {
			t.Fatal("unsafe display filename")
		}
		var rec fileRecord
		if err = db.Get(ctx, "file", meta.ID, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Path != meta.ID+".bin" || filepath.IsAbs(rec.Path) {
			t.Fatal("disk filename is not an opaque relative name")
		}
		for _, caller := range []struct {
			platform, user string
			allowed        bool
		}{{"api", "owner-a", true}, {"api", "owner-b", false}, {"api", "", false}, {"", "", true}} {
			f, _, e := s.OpenFile(ctx, meta.ID, caller.platform, caller.user)
			if !caller.allowed {
				if f != nil {
					f.Close()
				}
				if !errors.Is(e, sql.ErrNoRows) {
					t.Fatal("foreign key read was not denied")
				}
				continue
			}
			if e != nil {
				t.Fatal(e)
			}
			b, e := io.ReadAll(f)
			f.Close()
			if e != nil || string(b) != "generated payload" {
				t.Fatal("allowed file read failed")
			}
		}
	}
}

func TestLegacyFileIsAdminOnlyAndFailedPersistenceCleansBytes(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	legacy := filepath.Join(s.dataDir, "files", "legacy.txt")
	if err := os.WriteFile(legacy, []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(ctx, "file", "legacy", fileRecord{File: File{ID: "legacy"}, Path: legacy}, nil); err != nil {
		t.Fatal(err)
	}
	if f, _, err := s.OpenFile(ctx, "legacy", "api", "any-key"); err == nil {
		f.Close()
		t.Fatal("legacy ownerless file was exposed to API")
	}
	f, _, err := s.OpenFile(ctx, "legacy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	before, err := os.ReadDir(filepath.Join(s.dataDir, "files"))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.storeGeneratedFile(cancelled, "output.txt", "text/plain", []byte("must not remain"), "api", "owner"); err == nil {
		t.Fatal("cancelled persistence succeeded")
	}
	after, err := os.ReadDir(filepath.Join(s.dataDir, "files"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatal("orphan file left by persistence failure")
	}
}

func TestFileRejectsOutsideAndSymlinkTargets(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("outside sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(ctx, "file", "outside", fileRecord{File: File{ID: "outside", Filename: "private.txt"}, Path: outside}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.File(ctx, "outside"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("outside path must be unavailable")
	}
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(s.dataDir, "files", "linked.txt")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unavailable on host: %v", err)
		}
		if err := db.Put(ctx, "file", "linked", fileRecord{File: File{ID: "linked"}, Path: link}, nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.File(ctx, "linked"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("symlink must not expose external file")
		}
	})
}

func TestBackupRoundTripAcceptsExistingGeneratedFile(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	path := filepath.Join(s.dataDir, "files", "artifact.txt")
	if err := os.WriteFile(path, []byte("generated bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(ctx, "file", "artifact", fileRecord{File: File{ID: "artifact", Filename: "artifact.txt"}, Path: path}, nil); err != nil {
		t.Fatal(err)
	}
	pass := "synthetic-backup-password"
	out, err := s.Action(ctx, "backup.export", map[string]any{"passphrase": pass})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Action(ctx, "backup.import", map[string]any{"passphrase": pass, "blob": out.(map[string]any)["blob"]}); err != nil {
		t.Fatalf("own backup rejected: %v", err)
	}
	got, _, err := s.File(ctx, "artifact")
	if err != nil || got != path {
		t.Fatal("restored file is unavailable")
	}
}

func TestRelativeFileBackupRemainsPortableWithSeparateBytes(t *testing.T) {
	s, _ := testService(t)
	meta, err := s.storeGeneratedFile(context.Background(), "portable.txt", "text/plain", []byte("portable artifact"), "web", "admin")
	if err != nil {
		t.Fatal(err)
	}
	pass := "synthetic-backup-password"
	out, err := s.Action(context.Background(), "backup.export", map[string]any{"passphrase": pass})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testService(t)
	if _, err = target.Action(context.Background(), "backup.import", map[string]any{"passphrase": pass, "blob": out.(map[string]any)["blob"]}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = target.OpenFile(context.Background(), meta.ID, "", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("metadata-only restore claimed missing bytes were present")
	}
	if err = os.WriteFile(filepath.Join(target.dataDir, "files", meta.ID+".bin"), []byte("portable artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	f, _, err := target.OpenFile(context.Background(), meta.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "portable artifact" {
		t.Fatal("portable artifact could not be read")
	}
}

func TestLegacyAbsoluteFileBackupMigratesToDifferentDataDirectory(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	name := "legacyid-original.txt"
	oldPath := filepath.Join(s.dataDir, "files", name)
	if err := os.WriteFile(oldPath, []byte("migrated artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Put(ctx, "file", "legacyid", fileRecord{File: File{ID: "legacyid", Filename: "original.txt"}, Path: oldPath}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "personas.save", map[string]any{"id": "migrated", "name": "Migrated", "prompt": "Hello"}); err != nil {
		t.Fatal(err)
	}
	pass := "synthetic-backup-password"
	backup, err := s.Action(ctx, "backup.export", map[string]any{"passphrase": pass})
	if err != nil {
		t.Fatal(err)
	}
	target, targetDB := testService(t)
	if _, err = target.Action(ctx, "backup.import", map[string]any{"passphrase": pass, "blob": backup.(map[string]any)["blob"]}); err != nil {
		t.Fatalf("legacy migration rejected: %v", err)
	}
	var persona personaRecord
	if err = targetDB.Get(ctx, "persona", "migrated", &persona); err != nil || persona.Name != "Migrated" {
		t.Fatal("legacy media blocked unrelated records")
	}
	if f, _, err := target.OpenFile(ctx, "legacyid", "", ""); err == nil {
		f.Close()
		t.Fatal("migration read original machine path")
	}
	if err = os.WriteFile(filepath.Join(target.dataDir, "files", name), []byte("copied artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	f, _, err := target.OpenFile(ctx, "legacyid", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "copied artifact" {
		t.Fatal("migration did not use current data directory")
	}
}

func TestBackupRejectsUnsafeFileBeforeAnyMutation(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	if err := db.Put(ctx, "persona", "retained", personaRecord{ID: "retained", Name: "Original"}, nil); err != nil {
		t.Fatal(err)
	}
	persona, _ := json.Marshal(personaRecord{ID: "retained", Name: "Replaced"})
	bad, _ := json.Marshal(fileRecord{File: File{ID: "bad"}, Path: "../outside.txt"})
	plain, _ := json.Marshal(backupEnvelope{Version: 1, Records: []backupRecord{{Kind: "persona", ID: "retained", Data: persona}, {Kind: "file", ID: "bad", Data: bad}}})
	pass := "synthetic-backup-password"
	blob, err := secure.BackupEncrypt(pass, plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Action(ctx, "backup.import", map[string]any{"passphrase": pass, "blob": blob}); err == nil {
		t.Error("unsafe file backup accepted")
	}
	var got personaRecord
	if err = db.Get(ctx, "persona", "retained", &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Original" {
		t.Error("failed backup mutated earlier records")
	}
}
