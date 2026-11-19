package core

import (
	"context"
	"errors"
	"testing"
)

func TestCancelledSettingsUpdateAfterHookRestoresRuntimeAndDurableValues(t *testing.T) {
	s, db := testService(t)
	ctx, cancel := context.WithCancel(context.Background())
	var observed []string
	s.SetSettingsHook(func(values map[string]any) error {
		mode, _ := values["telegram.mode"].(string)
		observed = append(observed, mode)
		if mode == "webhook" {
			cancel()
		}
		return nil
	})
	if _, err := s.Action(ctx, "settings.update", map[string]any{"telegram.mode": "webhook"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled update error=%v", err)
	}
	if got := s.RuntimeSettings()["telegram.mode"]; got != "polling" {
		t.Fatalf("runtime mode after failed update=%v", got)
	}
	if len(observed) != 2 || observed[0] != "webhook" || observed[1] != "polling" {
		t.Fatalf("hook application/rollback=%#v", observed)
	}
	var persisted map[string]any
	if err := db.Get(context.Background(), "settings", "current", &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["telegram.mode"] != "polling" {
		t.Fatalf("durable mode after cancelled update=%v", persisted["telegram.mode"])
	}
}

func TestCancelledSettingsUpdateDoesNotInvokeRuntimeHook(t *testing.T) {
	s, _ := testService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	s.SetSettingsHook(func(map[string]any) error { called = true; return nil })
	if _, err := s.Action(ctx, "settings.update", map[string]any{"telegram.mode": "webhook"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled update error=%v", err)
	}
	if called {
		t.Fatal("cancelled settings update invoked runtime hook")
	}
	if s.RuntimeSettings()["telegram.mode"] != "polling" {
		t.Fatal("cancelled settings update changed runtime settings")
	}
}

func TestSettingsHookFailureRollsBackRawSettings(t *testing.T) {
	s, _ := testService(t)
	before := s.Settings()
	s.SetSettingsHook(func(map[string]any) error { return errors.New("runtime reload failed") })
	if _, err := s.Action(context.Background(), "settings.update", map[string]any{"values": map[string]any{"telegram.mode": "webhook"}}); err == nil {
		t.Fatal("hook failure was accepted")
	}
	after := s.Settings()
	if after["telegram.mode"] != before["telegram.mode"] {
		t.Fatalf("settings were not rolled back: before=%#v after=%#v", before["telegram.mode"], after["telegram.mode"])
	}
}
