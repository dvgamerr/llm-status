package model

import (
	"testing"
	"time"
)

func usageSnapshot(sessionID string, capturedAt time.Time) Snapshot {
	usedPercentage, fiveHour := 42.0, 7.5
	resetsAt, totalInput, totalOutput := int64(1787994600), int64(80_000), int64(9_000)
	cost := 1.25
	thinking := true
	return Snapshot{
		SchemaVersion:     CurrentSchemaVersion,
		CapturedAt:        capturedAt,
		Provider:          ProviderClaude,
		ClientVersion:     "2.1.248",
		ClaudeCodeVersion: "2.1.248",
		Session:           Session{ID: sessionID, Name: "terminal session"},
		Model:             Model{ID: "claude-opus-5", DisplayName: "Opus 5"},
		Context: Context{
			UsedPercentage:    &usedPercentage,
			TotalInputTokens:  &totalInput,
			TotalOutputTokens: &totalOutput,
		},
		RateLimits: RateLimits{
			FiveHour: RateWindow{UsedPercentage: &fiveHour, ResetsAt: &resetsAt},
		},
		Cost:            Cost{TotalCostUSD: &cost},
		Effort:          "high",
		ThinkingEnabled: &thinking,
	}
}

// hookOnlySnapshot is the shape a Claude Code session inside the VS Code
// extension produces: the activity hook wrote it, statusLine never ran.
func hookOnlySnapshot(sessionID string, capturedAt time.Time) Snapshot {
	return Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		CapturedAt:    capturedAt,
		Provider:      ProviderClaude,
		Session:       Session{ID: sessionID},
		Activity:      Activity{State: ActivityTyping, UpdatedAt: capturedAt},
	}
}

func TestHasUsage(t *testing.T) {
	now := time.Date(2026, time.August, 29, 13, 0, 0, 0, time.UTC)
	if !usageSnapshot("cli", now).HasUsage() {
		t.Fatal("statusLine snapshot reported no usage")
	}
	if hookOnlySnapshot("vscode", now).HasUsage() {
		t.Fatal("hook-only snapshot reported usage")
	}

	windowSize := int64(1_000_000)
	contextOnly := hookOnlySnapshot("vscode", now)
	contextOnly.Context.WindowSize = &windowSize
	if !contextOnly.HasUsage() {
		t.Fatal("context window size alone did not count as usage")
	}

	limitsOnly := hookOnlySnapshot("vscode", now)
	limitsOnly.RateLimits.Plan = "max"
	if !limitsOnly.HasUsage() {
		t.Fatal("a reported plan alone did not count as usage")
	}
}

func TestWithUsageFromKeepsIdentityAndFillsNumbers(t *testing.T) {
	now := time.Date(2026, time.August, 29, 13, 0, 0, 0, time.UTC)
	donor := usageSnapshot("cli", now.Add(-time.Hour))
	base := hookOnlySnapshot("vscode", now)

	merged := base.WithUsageFrom(donor)

	if merged.Session.ID != "vscode" || merged.Session.Name != "" {
		t.Fatalf("session identity was overwritten: %+v", merged.Session)
	}
	if !merged.CapturedAt.Equal(now) || merged.Activity != base.Activity {
		t.Fatalf("activity or capture time was overwritten: %+v", merged)
	}
	if merged.Model.ID != "claude-opus-5" || merged.ClientVersion != "2.1.248" {
		t.Fatalf("model identity was not backfilled: %+v", merged)
	}
	if !merged.Context.HasValues() || !merged.RateLimits.HasValues() || !merged.Cost.HasValues() {
		t.Fatalf("numbers were not backfilled: %+v", merged)
	}
	if merged.Effort != "high" || merged.ThinkingEnabled == nil || !*merged.ThinkingEnabled {
		t.Fatalf("scalar fields were not backfilled: %+v", merged)
	}
}

func TestWithUsageFromNeverOverwritesOwnValues(t *testing.T) {
	now := time.Date(2026, time.August, 29, 13, 0, 0, 0, time.UTC)
	base := usageSnapshot("cli", now)
	donor := usageSnapshot("other", now.Add(-time.Hour))
	donor.Model.ID = "claude-sonnet-5"
	donor.Effort = "low"
	otherLimit := 99.0
	donor.RateLimits.FiveHour.UsedPercentage = &otherLimit

	merged := base.WithUsageFrom(donor)

	if merged.Model.ID != "claude-opus-5" || merged.Effort != "high" {
		t.Fatalf("donor overwrote existing identity: %+v", merged)
	}
	if got := merged.RateLimits.FiveHour.UsedPercentage; got == nil || *got != 7.5 {
		t.Fatalf("donor overwrote existing rate limits: %v", got)
	}
}

func TestLatestWithUsageBackfillsHookOnlyWinner(t *testing.T) {
	now := time.Date(2026, time.August, 29, 13, 0, 0, 0, time.UTC)
	stale := usageSnapshot("cli-old", now.Add(-2*time.Hour))
	stale.Effort = "medium"
	fresh := usageSnapshot("cli-new", now.Add(-time.Hour))
	vscode := hookOnlySnapshot("vscode", now)

	merged, ok := LatestWithUsage([]Snapshot{stale, fresh, vscode})
	if !ok {
		t.Fatal("LatestWithUsage found nothing")
	}
	if merged.Session.ID != "vscode" || merged.Activity.State != ActivityTyping {
		t.Fatalf("newest snapshot did not win: %+v", merged)
	}
	// cli-new, not cli-old, is the newest snapshot that carries numbers.
	if merged.Effort != "high" {
		t.Fatalf("backfilled from the wrong donor: effort = %q", merged.Effort)
	}
}

func TestLatestWithUsageLeavesOrdinaryWinnerUnchanged(t *testing.T) {
	now := time.Date(2026, time.August, 29, 13, 0, 0, 0, time.UTC)
	older := hookOnlySnapshot("vscode", now.Add(-time.Hour))
	newest := usageSnapshot("cli", now)

	merged, ok := LatestWithUsage([]Snapshot{older, newest})
	if !ok {
		t.Fatal("LatestWithUsage found nothing")
	}
	if merged.Session.ID != "cli" {
		t.Fatalf("newest snapshot did not win: %+v", merged)
	}
}

func TestLatestWithUsageWithNoDonorAndEmptyGroup(t *testing.T) {
	now := time.Date(2026, time.August, 29, 13, 0, 0, 0, time.UTC)
	merged, ok := LatestWithUsage([]Snapshot{hookOnlySnapshot("vscode", now)})
	if !ok {
		t.Fatal("LatestWithUsage found nothing")
	}
	if merged.HasUsage() {
		t.Fatalf("invented usage with no donor available: %+v", merged)
	}
	if _, ok := LatestWithUsage(nil); ok {
		t.Fatal("empty group reported a selection")
	}
}
