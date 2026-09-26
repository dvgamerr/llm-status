package usage

import (
	"testing"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/state"
)

func TestRunMergesRateLimitsWithoutTouchingOtherFields(t *testing.T) {
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seeded := model.Snapshot{
		SchemaVersion: model.CurrentSchemaVersion,
		CapturedAt:    time.Now(),
		Provider:      "claude",
		Session:       model.Session{ID: "abc", Name: "demo"},
		Model:         model.Model{DisplayName: "Sonnet 5"},
		Activity:      model.Activity{State: model.ActivityWorking, UpdatedAt: time.Now()},
	}
	if saveErr := store.Save(seeded); saveErr != nil {
		t.Fatal(saveErr)
	}

	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	snapshot, err := Run(store, "abc", 63, 42, 5*time.Hour, 168*time.Hour, now)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if *snapshot.RateLimits.FiveHour.UsedPercentage != 63 {
		t.Fatalf("FiveHour.UsedPercentage = %v", *snapshot.RateLimits.FiveHour.UsedPercentage)
	}
	if *snapshot.RateLimits.SevenDay.UsedPercentage != 42 {
		t.Fatalf("SevenDay.UsedPercentage = %v", *snapshot.RateLimits.SevenDay.UsedPercentage)
	}
	wantFiveReset := now.Add(5 * time.Hour).Unix()
	if *snapshot.RateLimits.FiveHour.ResetsAt != wantFiveReset {
		t.Fatalf("FiveHour.ResetsAt = %v, want %v", *snapshot.RateLimits.FiveHour.ResetsAt, wantFiveReset)
	}
	wantSevenReset := now.Add(168 * time.Hour).Unix()
	if *snapshot.RateLimits.SevenDay.ResetsAt != wantSevenReset {
		t.Fatalf("SevenDay.ResetsAt = %v, want %v", *snapshot.RateLimits.SevenDay.ResetsAt, wantSevenReset)
	}

	if snapshot.Model.DisplayName != "Sonnet 5" || snapshot.Session.Name != "demo" {
		t.Fatalf("Run() clobbered unrelated fields: %+v", snapshot)
	}
	if snapshot.Activity.State != model.ActivityWorking {
		t.Fatalf("Run() clobbered activity: %+v", snapshot.Activity)
	}
}

func TestRunDefaultsToTheMostRecentlyUpdatedSession(t *testing.T) {
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := store.Save(model.Snapshot{
		SchemaVersion: model.CurrentSchemaVersion, CapturedAt: time.Now(), Session: model.Session{ID: "latest-one"},
	}); saveErr != nil {
		t.Fatal(saveErr)
	}

	snapshot, err := Run(store, "", 10, 20, time.Hour, 24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if snapshot.Session.ID != "latest-one" {
		t.Fatalf("Run() updated session %q, want %q", snapshot.Session.ID, "latest-one")
	}
}

func TestRunClampsOutOfRangePercentages(t *testing.T) {
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := store.Save(model.Snapshot{SchemaVersion: model.CurrentSchemaVersion, CapturedAt: time.Now(), Session: model.Session{ID: "abc"}}); saveErr != nil {
		t.Fatal(saveErr)
	}
	snapshot, err := Run(store, "abc", 150, -5, time.Hour, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if *snapshot.RateLimits.FiveHour.UsedPercentage != 100 {
		t.Fatalf("FiveHour.UsedPercentage = %v, want clamped to 100", *snapshot.RateLimits.FiveHour.UsedPercentage)
	}
	if *snapshot.RateLimits.SevenDay.UsedPercentage != 0 {
		t.Fatalf("SevenDay.UsedPercentage = %v, want clamped to 0", *snapshot.RateLimits.SevenDay.UsedPercentage)
	}
}

func TestRunReturnsErrorWhenNoSnapshotExists(t *testing.T) {
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(store, "missing", 1, 1, time.Hour, time.Hour, time.Now()); err == nil {
		t.Fatal("expected an error when the target session has no existing snapshot")
	}
}

func TestRunValidatesDependenciesAndResetDurations(t *testing.T) {
	now := time.Now()
	if _, err := Run(nil, "", 1, 1, time.Hour, time.Hour, now); err == nil {
		t.Fatal("Run() accepted nil store")
	}
	store, err := state.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(store, "", 1, 1, time.Hour, time.Hour, time.Time{}); err == nil {
		t.Fatal("Run() accepted zero time")
	}
	if _, err := Run(store, "", 1, 1, 0, time.Hour, now); err == nil {
		t.Fatal("Run() accepted a non-positive reset duration")
	}
}
