// Package usage merges manually-supplied 5h/7d rate-limit percentages into
// an already-stored snapshot. It exists for interfaces where Claude Code's
// statusLine command is never invoked (this project has only ever seen
// that happen from a real CLI terminal, never from the VS Code extension
// chat panel) — a manual stand-in for the numbers statusLine would
// otherwise report, read off the Account & Usage panel by hand.
package usage

import (
	"errors"
	"fmt"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/sanitize"
	"github.com/dvgamerr/llm-status/internal/state"
)

// Run loads the target snapshot (by session ID, or the most recently
// updated session if sessionID is empty), overwrites only its RateLimits,
// and saves it back — model, context, session, and activity are left
// exactly as they were.
func Run(store *state.Store, sessionID string, fiveHourPercent, sevenDayPercent float64, fiveHourReset, sevenDayReset time.Duration, now time.Time) (model.Snapshot, error) {
	if store == nil {
		return model.Snapshot{}, errors.New("snapshot store is nil")
	}
	if now.IsZero() {
		return model.Snapshot{}, errors.New("usage time is empty")
	}
	if fiveHourReset <= 0 || sevenDayReset <= 0 {
		return model.Snapshot{}, errors.New("rate-limit reset durations must be greater than zero")
	}
	snapshot, err := load(store, sessionID)
	if err != nil {
		return model.Snapshot{}, fmt.Errorf("load existing snapshot: %w", err)
	}

	five := sanitize.ClampPercentage(fiveHourPercent)
	seven := sanitize.ClampPercentage(sevenDayPercent)
	fiveResetAt := now.Add(fiveHourReset).Unix()
	sevenResetAt := now.Add(sevenDayReset).Unix()
	snapshot.RateLimits.FiveHour = model.RateWindow{UsedPercentage: &five, ResetsAt: &fiveResetAt}
	snapshot.RateLimits.SevenDay = model.RateWindow{UsedPercentage: &seven, ResetsAt: &sevenResetAt}

	if err := store.Save(snapshot); err != nil {
		return model.Snapshot{}, fmt.Errorf("save snapshot: %w", err)
	}
	return snapshot, nil
}

func load(store *state.Store, sessionID string) (model.Snapshot, error) {
	if sessionID == "" {
		return store.LoadLatest()
	}
	return store.LoadSession(sessionID)
}
