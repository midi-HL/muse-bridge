package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/media"
)

func TestASRUsesUpdatedRuntimeSettings(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MUSE_TEST_ASR_PROCESS", "1")
	arguments, err := json.Marshal([]string{"-test.run=^TestASRRuntimeHelperProcess$", "--", "{audio}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"values": map[string]any{
		"asr.provider": "local", "asr.executable": executable, "asr.model_dir": "synthetic-model", "asr.arguments": string(arguments),
	}}); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"filename": "test.wav", "data_base64": base64.StdEncoding.EncodeToString([]byte("synthetic audio bytes"))}
	result, err := s.Action(ctx, "asr.test", input)
	if err != nil {
		t.Fatalf("saved local ASR settings did not take effect: %v", err)
	}
	if result.(media.Transcript).Text != "synthetic transcript" {
		t.Fatalf("unexpected transcript: %#v", result)
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"values": map[string]any{"asr.provider": "disabled"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(ctx, "asr.test", input); !errors.Is(err, media.ErrNotConfigured) {
		t.Fatalf("disabled ASR should stop processing immediately, got %v", err)
	}
}

func TestASRRuntimeHelperProcess(t *testing.T) {
	if os.Getenv("MUSE_TEST_ASR_PROCESS") != "1" {
		return
	}
	input, err := os.ReadFile(os.Args[len(os.Args)-1])
	if err != nil || string(input) != "synthetic audio bytes" {
		os.Exit(2)
	}
	fmt.Print("synthetic transcript")
	os.Exit(0)
}
