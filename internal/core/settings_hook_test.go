package core

import (
	"context"
	"errors"
	"testing"
)

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
