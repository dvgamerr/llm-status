package ingest

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/state"
)

func TestRunPersistsSanitizedSnapshotAndWritesStatusLine(t *testing.T) {
	store, err := state.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader(`{
      "session_id":"ingest-test",
      "model":{"display_name":"Opus"},
      "context_window":{"used_percentage":42},
      "transcript_path":"/must/not/be/stored"
    }`)
	var output bytes.Buffer
	now := time.Date(2026, 7, 31, 14, 0, 0, 0, time.UTC)

	if _, runErr := Run(input, &output, store, now); runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	}
	if got := strings.TrimSpace(output.String()); got != "[Opus] ctx 42%" {
		t.Fatalf("status line = %q", got)
	}
	snapshot, err := store.LoadLatest()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.ID != "ingest-test" || !snapshot.CapturedAt.Equal(now) {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestRunPreservesActivityAcrossStatusLineRefresh(t *testing.T) {
	store, err := state.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 31, 14, 0, 0, 0, time.UTC)
	first := strings.NewReader(`{"session_id":"activity-test","context_window":{"used_percentage":10}}`)
	if _, runErr := Run(first, &bytes.Buffer{}, store, now); runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	}
	snapshot, err := store.LoadSession("activity-test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Activity = model.Activity{State: model.ActivityWorking, UpdatedAt: now}
	if saveErr := store.Save(snapshot); saveErr != nil {
		t.Fatal(saveErr)
	}

	second := strings.NewReader(`{"session_id":"activity-test","context_window":{"used_percentage":20}}`)
	if _, runErr := Run(second, &bytes.Buffer{}, store, now.Add(5*time.Second)); runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	}
	after, err := store.LoadSession("activity-test")
	if err != nil {
		t.Fatal(err)
	}
	if after.Activity.State != model.ActivityWorking {
		t.Fatalf("Activity.State = %q, want %q (statusLine refresh must not erase it)", after.Activity.State, model.ActivityWorking)
	}
}

func TestRunReturnsDecodeAndOutputErrors(t *testing.T) {
	store, err := state.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(strings.NewReader("not-json"), &bytes.Buffer{}, store, time.Now()); err == nil {
		t.Fatal("Run() accepted invalid JSON")
	}

	valid := strings.NewReader(`{"session_id":"output-error"}`)
	if _, err := Run(valid, errorWriter{}, store, time.Now()); err == nil || !strings.Contains(err.Error(), "write status line") {
		t.Fatalf("Run() output error = %v", err)
	}
}

func TestRunRejectsNilDependencies(t *testing.T) {
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	valid := strings.NewReader(`{"session_id":"session"}`)
	if _, err := Run(nil, io.Discard, store, time.Now()); err == nil {
		t.Fatal("Run() accepted nil input")
	}
	if _, err := Run(valid, nil, store, time.Now()); err == nil {
		t.Fatal("Run() accepted nil output")
	}
	if _, err := Run(strings.NewReader(`{"session_id":"session"}`), io.Discard, nil, time.Now()); err == nil {
		t.Fatal("Run() accepted nil store")
	}
}

func TestRunSurfacesCorruptPriorActivity(t *testing.T) {
	dir := t.TempDir()
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	seed := model.Snapshot{SchemaVersion: model.CurrentSchemaVersion, CapturedAt: time.Now(), Session: model.Session{ID: "session"}}
	if saveErr := store.Save(seed); saveErr != nil {
		t.Fatal(saveErr)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "sessions"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir() entries=%d error=%v", len(entries), err)
	}
	if writeErr := os.WriteFile(filepath.Join(dir, "sessions", entries[0].Name()), []byte("broken"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, err = Run(strings.NewReader(`{"session_id":"session"}`), io.Discard, store, time.Now())
	if err == nil || !strings.Contains(err.Error(), "load prior activity") {
		t.Fatalf("Run() error = %v", err)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
