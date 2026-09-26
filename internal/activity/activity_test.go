package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/state"
)

func TestRunCreatesSkeletonSnapshotWhenSessionUnknown(t *testing.T) {
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	snapshot, matched, err := Run(strings.NewReader(`{"session_id":"abc","hook_event_name":"PreToolUse"}`), store, now)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !matched {
		t.Fatal("expected matched = true")
	}
	if snapshot.Activity.State != model.ActivityTyping {
		t.Fatalf("Activity.State = %q", snapshot.Activity.State)
	}
	if snapshot.Session.ID != "abc" || snapshot.Provider != "claude" {
		t.Fatalf("unexpected skeleton snapshot: %+v", snapshot)
	}
}

func TestRunTracksConcurrentSubagentCount(t *testing.T) {
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	snapshot, matched, err := Run(strings.NewReader(`{"session_id":"abc","hook_event_name":"SubagentStart"}`), store, now)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !matched || snapshot.Activity.Subagents != 1 {
		t.Fatalf("after first SubagentStart: matched=%v subagents=%d", matched, snapshot.Activity.Subagents)
	}

	snapshot, matched, err = Run(strings.NewReader(`{"session_id":"abc","hook_event_name":"SubagentStart"}`), store, now)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !matched || snapshot.Activity.Subagents != 2 {
		t.Fatalf("after second SubagentStart: matched=%v subagents=%d", matched, snapshot.Activity.Subagents)
	}

	snapshot, matched, err = Run(strings.NewReader(`{"session_id":"abc","hook_event_name":"SubagentStop"}`), store, now)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !matched || snapshot.Activity.Subagents != 1 {
		t.Fatalf("after first SubagentStop: matched=%v subagents=%d", matched, snapshot.Activity.Subagents)
	}

	snapshot, matched, err = Run(strings.NewReader(`{"session_id":"abc","hook_event_name":"SubagentStop"}`), store, now)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !matched || snapshot.Activity.Subagents != 0 {
		t.Fatalf("after second SubagentStop: matched=%v subagents=%d", matched, snapshot.Activity.Subagents)
	}

	// A third stop with no matching start must not go negative.
	snapshot, matched, err = Run(strings.NewReader(`{"session_id":"abc","hook_event_name":"SubagentStop"}`), store, now)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !matched || snapshot.Activity.Subagents != 0 {
		t.Fatalf("unmatched SubagentStop went negative: matched=%v subagents=%d", matched, snapshot.Activity.Subagents)
	}
}

func TestRunIgnoresUnmappedHookEvents(t *testing.T) {
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, matched, err := Run(strings.NewReader(`{"session_id":"abc","hook_event_name":"SessionStart"}`), store, time.Now())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if matched {
		t.Fatal("expected matched = false for an unmapped hook event")
	}
	if _, err := store.LoadSession("abc"); err == nil {
		t.Fatal("expected no snapshot to be saved for an unmapped hook event")
	}
}

func TestRunPreservesExistingSnapshotFields(t *testing.T) {
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pct := 42.0
	seeded := model.Snapshot{
		SchemaVersion: model.CurrentSchemaVersion,
		CapturedAt:    time.Now(),
		Provider:      "claude",
		Session:       model.Session{ID: "abc", Name: "demo"},
		Model:         model.Model{DisplayName: "Opus"},
		Context:       model.Context{UsedPercentage: &pct},
	}
	if saveErr := store.Save(seeded); saveErr != nil {
		t.Fatal(saveErr)
	}

	snapshot, matched, err := Run(strings.NewReader(`{"session_id":"abc","hook_event_name":"Stop"}`), store, time.Now())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !matched {
		t.Fatal("expected matched = true")
	}
	if snapshot.Activity.State != model.ActivityIdle {
		t.Fatalf("Activity.State = %q", snapshot.Activity.State)
	}
	if snapshot.Session.Name != "demo" || snapshot.Model.DisplayName != "Opus" || *snapshot.Context.UsedPercentage != pct {
		t.Fatalf("Run() clobbered existing snapshot fields: %+v", snapshot)
	}
}

func TestRunValidatesDependenciesAndNormalizesTime(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.FixedZone("ICT", 7*60*60))
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, runErr := Run(nil, store, now); runErr == nil {
		t.Fatal("Run() accepted nil input")
	}
	if _, _, runErr := Run(strings.NewReader(`{}`), nil, now); runErr == nil {
		t.Fatal("Run() accepted nil store")
	}
	if _, _, runErr := Run(strings.NewReader(`{}`), store, time.Time{}); runErr == nil {
		t.Fatal("Run() accepted zero time")
	}
	snapshot, _, err := Run(strings.NewReader(`{"session_id":"tz","hook_event_name":"Stop"}`), store, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CapturedAt.Location() != time.UTC || snapshot.Activity.UpdatedAt.Location() != time.UTC {
		t.Fatalf("times were not normalized to UTC: captured=%v activity=%v", snapshot.CapturedAt, snapshot.Activity.UpdatedAt)
	}
}
