package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/secure"
)

func TestBackupImportReportsRestartForRuntimeOwnedRecords(t *testing.T) {
	source, _ := testService(t)
	ctx := context.Background()
	if _, err := source.Action(ctx, "accounts.import", map[string]any{"id": "restored-account", "cookies": map[string]string{"hatch_sess": "synthetic-backup-cookie"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Action(ctx, "settings.update", map[string]any{"values": map[string]any{"limits.daily_per_user": 17}}); err != nil {
		t.Fatal(err)
	}
	const passphrase = "synthetic-backup-passphrase"
	exported, err := source.Action(ctx, "backup.export", map[string]any{"passphrase": passphrase})
	if err != nil {
		t.Fatal(err)
	}
	target, db := testService(t)
	result, err := target.Action(ctx, "backup.import", map[string]any{"passphrase": passphrase, "blob": exported.(map[string]any)["blob"]})
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]any
	if err := db.Get(ctx, "account", "restored-account", &restored); err != nil {
		t.Fatalf("import did not persist the restored account: %v", err)
	}
	// The active pool and settings remain the pre-import snapshot. The response
	// must tell callers to restart rather than claim those live values changed.
	if len(target.pool.List()) != 0 || settingInt(target.Settings(), "limits.daily_per_user", 0) != 100 {
		t.Fatal("test precondition changed: import now reloads runtime-owned records")
	}
	if result.(map[string]any)["requires_restart"] != true {
		t.Fatalf("import persisted runtime-owned records without a restart notice: %#v", result)
	}
}

func TestBackupImportDatabaseOnlyRecordsNeedNoRestart(t *testing.T) {
	ctx := context.Background()
	data, err := json.Marshal(personaRecord{ID: "restored-persona", Name: "Restored", Prompt: "Be concise.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(backupEnvelope{Version: 1, Records: []backupRecord{{Kind: "persona", ID: "restored-persona", Data: data}}})
	if err != nil {
		t.Fatal(err)
	}
	const passphrase = "synthetic-database-only-passphrase"
	blob, err := secure.BackupEncrypt(passphrase, plain)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := testService(t)
	result, err := target.Action(ctx, "backup.import", map[string]any{"passphrase": passphrase, "blob": blob})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["requires_restart"] != false {
		t.Fatalf("database-only restore should not request restart: %#v", result)
	}
	if prompt, err := target.personaPrompt(ctx, "restored-persona"); err != nil || prompt != "Be concise." {
		t.Fatalf("database-only restore not immediately visible: %q %v", prompt, err)
	}
}
