package core

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/midi-HL/muse-bridge/internal/hatch"
)

func captureGeneration(t *testing.T, s *Service) *[]hatch.GenerateRequest {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Action(ctx, "accounts.import", map[string]any{"id": "synthetic", "cookies": map[string]string{"hatch_sess": "synthetic-only"}}); err != nil {
		t.Fatal(err)
	}
	requests := []hatch.GenerateRequest{}
	s.generate = func(_ context.Context, _ *hatch.Client, req hatch.GenerateRequest, emit func(hatch.Event) error) (hatch.GenerateResult, error) {
		requests = append(requests, req)
		if err := emit(hatch.Event{Type: "text", Text: "generated"}); err != nil {
			return hatch.GenerateResult{}, err
		}
		return hatch.GenerateResult{NodeID: req.NodeID, Text: "generated"}, nil
	}
	return &requests
}

func saveTestPersona(t *testing.T, s *Service, id, prompt string) {
	t.Helper()
	if _, err := s.Action(context.Background(), "personas.save", map[string]any{"id": id, "name": id, "prompt": prompt}); err != nil {
		t.Fatal(err)
	}
}

func TestChatResolvesPersonaPromptAndPinsDefault(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	requests := captureGeneration(t, s)
	saveTestPersona(t, s, "calm", "Answer calmly in Chinese.")
	saveTestPersona(t, s, "brief", "Use one sentence.")
	if _, err := s.Action(ctx, "settings.update", map[string]any{"persona.default_id": "calm"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "first", Platform: "web", UserID: "admin", Prompt: "hi"}, nil); err != nil {
		t.Fatal(err)
	}
	if (*requests)[0].Persona != "Answer calmly in Chinese." {
		t.Error("default persona prompt did not reach generation")
	}
	if _, err := s.Action(ctx, "settings.update", map[string]any{"persona.default_id": "brief"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "first", Platform: "web", UserID: "admin", Prompt: "continue"}, nil); err != nil {
		t.Fatal(err)
	}
	if (*requests)[1].Persona != "Answer calmly in Chinese." {
		t.Error("default change unexpectedly changed existing session")
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "second", Platform: "web", UserID: "admin", Prompt: "hi", PersonaID: "calm"}, nil); err != nil {
		t.Fatal(err)
	}
	if (*requests)[2].Persona != "Answer calmly in Chinese." {
		t.Error("persona identifier sent instead of saved prompt")
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "first", Platform: "web", UserID: "admin", Prompt: "continue", PersonaID: "_none"}, nil); err != nil {
		t.Fatal(err)
	}
	if (*requests)[3].Persona != "" {
		t.Error("explicit no-persona selection was ignored")
	}
}

func TestInvalidPersonaRejectsBeforeGenerationOrHistoryMutation(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	requests := captureGeneration(t, s)
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "keep", Platform: "web", UserID: "admin", Prompt: "before"}, nil); err != nil {
		t.Fatal(err)
	}
	var before json.RawMessage
	if err := db.Get(ctx, "session", "keep", &before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "keep", Platform: "web", UserID: "admin", Prompt: "must not persist", PersonaID: "missing"}, nil); err == nil {
		t.Error("missing persona accepted")
	}
	var after json.RawMessage
	if err := db.Get(ctx, "session", "keep", &after); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("invalid persona mutated history")
	}
	if len(*requests) != 1 {
		t.Error("invalid persona invoked generator")
	}
	saveTestPersona(t, s, "disabled", "not active")
	if _, err := s.Action(ctx, "personas.save", map[string]any{"id": "disabled", "name": "disabled", "prompt": "not active", "enabled": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "keep", Platform: "web", UserID: "admin", Prompt: "no", PersonaID: "disabled"}, nil); err == nil {
		t.Error("disabled persona accepted")
	}
}

func TestPersonaSelectionSurvivesRestartAndEnforcesOwnership(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	saveTestPersona(t, s, "calm", "Calm style")
	if _, err := s.SetSessionPersona(ctx, "tg-one", "telegram", "owner", "calm"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSessionPersona(ctx, "tg-one", "telegram", "foreign", "_none"); err == nil {
		t.Fatal("foreign user switched persona")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(ctx, db, s.vault, s.redactor, s.dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var record sessionRecord
	if err = db.Get(ctx, "session", "tg-one", &record); err != nil {
		t.Fatal(err)
	}
	id, prompt, err := reopened.resolvePersona(ctx, "", record)
	if err != nil || id != "calm" || prompt != "Calm style" {
		t.Fatal("persisted persona selection lost")
	}
}

func TestActiveSessionCannotBeOverwrittenOrStoppedByReplacement(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	captureGeneration(t, s)
	saveTestPersona(t, s, "calm", "Calm")
	if err := db.Put(ctx, "session", "active", sessionRecord{ID: "active", Platform: "web", UserID: "admin"}, nil); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	unwind := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(unwind) }) }
	defer release()
	s.generate = func(c context.Context, _ *hatch.Client, _ hatch.GenerateRequest, _ func(hatch.Event) error) (hatch.GenerateResult, error) {
		close(started)
		<-c.Done()
		<-unwind
		return hatch.GenerateResult{}, c.Err()
	}
	go func() {
		_, err := s.Chat(ctx, ChatRequest{SessionID: "active", Platform: "web", UserID: "admin", Prompt: "one"}, nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("generator not started")
	}
	for _, args := range []map[string]any{{"id": "active", "persona_id": "calm"}} {
		if _, err := s.Action(ctx, "sessions.persona", args); err == nil {
			t.Error("changed active persona")
		}
	}
	if !s.Stop("active") {
		t.Fatal("active session not stopped")
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "active", Platform: "web", UserID: "admin", Prompt: "two"}, nil); err == nil {
		t.Error("replacement took cancelled but unfinished lease")
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled generation succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("generation did not unwind")
	}
}

func TestFailedGenerationDoesNotPersistTurnOrPersona(t *testing.T) {
	s, db := testService(t)
	ctx := context.Background()
	captureGeneration(t, s)
	saveTestPersona(t, s, "calm", "Calm answer")
	s.generate = func(context.Context, *hatch.Client, hatch.GenerateRequest, func(hatch.Event) error) (hatch.GenerateResult, error) {
		return hatch.GenerateResult{}, errors.New("synthetic upstream unavailable")
	}
	if _, err := s.Chat(ctx, ChatRequest{SessionID: "failed", Platform: "web", UserID: "admin", Prompt: "failed turn", PersonaID: "calm"}, nil); err == nil {
		t.Fatal("expected upstream failure")
	}
	var history sessionRecord
	if err := db.Get(ctx, "session", "failed", &history); err == nil {
		t.Fatal("failed generation persisted user turn/persona")
	}
}
