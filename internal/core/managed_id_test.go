package core

import (
	"context"
	"testing"
)

// Random IDs are raw URL Base64, whose first character may be '-' or '_'.
// These are valid opaque IDs, including when restoring an existing backup.
func TestURLBase64IDsWorkAcrossManagedActionsAndBackup(t *testing.T) {
	for _, id := range []string{"-Base64Identifier", "_Base64Identifier"} {
		t.Run(id[:1], func(t *testing.T) {
			s, _ := testService(t)
			ctx := context.Background()
			for _, action := range []struct {
				name string
				args map[string]any
			}{
				{"nodes.save", map[string]any{"id": id, "name": "Test node", "url": "http://127.0.0.1:18080"}},
				{"personas.save", map[string]any{"id": id, "name": "Test persona", "prompt": "Be concise."}},
				{"schedules.save", map[string]any{"id": id, "name": "Test schedule", "platform": "web", "target": "test-session", "prompt": "Hello", "cron": "0 9 * * *", "timezone": "UTC"}},
				{"accounts.import", map[string]any{"id": id, "cookies": map[string]string{"hatch_sess": "synthetic-session"}}},
			} {
				if _, err := s.Action(ctx, action.name, action.args); err != nil {
					t.Fatalf("%s rejected valid opaque ID %q: %v", action.name, id, err)
				}
			}
			const passphrase = "synthetic-backup-passphrase"
			exported, err := s.Action(ctx, "backup.export", map[string]any{"passphrase": passphrase})
			if err != nil {
				t.Fatal(err)
			}
			restored, _ := testService(t)
			if _, err := restored.Action(ctx, "backup.import", map[string]any{"passphrase": passphrase, "blob": exported.(map[string]any)["blob"]}); err != nil {
				t.Fatalf("backup rejected valid opaque ID %q: %v", id, err)
			}
			for _, name := range []string{"nodes.delete", "personas.delete", "schedules.delete"} {
				if _, err := restored.Action(ctx, name, map[string]any{"id": id}); err != nil {
					t.Fatalf("%s rejected valid opaque ID %q: %v", name, id, err)
				}
			}
		})
	}
}

func TestManagedIDsStillRejectPathsAndDotNames(t *testing.T) {
	for _, id := range []string{"", ".", "..", ".hidden", "../account", "account/other", "account\\other", "account\nother"} {
		if managedID.MatchString(id) {
			t.Errorf("accepted invalid managed ID %q", id)
		}
	}
}
