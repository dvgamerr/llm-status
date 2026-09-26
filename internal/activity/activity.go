// Package activity turns a Claude Code hook event into an update of the
// dashboard's working/idle/waiting-approval indicator, independent of the
// statusLine refresh cycle.
package activity

import (
	"errors"
	"io"
	"os"
	"time"

	"github.com/dvgamerr/llm-status/internal/claude"
	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/state"
)

// Run reads one Claude Code hook payload from input and, if it maps to a
// meaningful activity update, merges that update into the session's stored
// snapshot. A hook event can carry a new discrete State (UserPromptSubmit,
// PreToolUse, Stop, a permission Notification), a Subagents count delta
// (SubagentStart/SubagentStop), or both being unset — matched is false only
// when neither applies (for example an idle-nudge Notification); callers
// should treat that as a no-op, not an error.
func Run(input io.Reader, store *state.Store, now time.Time) (snapshot model.Snapshot, matched bool, err error) {
	if input == nil {
		return model.Snapshot{}, false, errors.New("hook input is nil")
	}
	if store == nil {
		return model.Snapshot{}, false, errors.New("snapshot store is nil")
	}
	if now.IsZero() {
		return model.Snapshot{}, false, errors.New("activity time is empty")
	}
	now = now.UTC()
	hook, err := claude.DecodeHook(input)
	if err != nil {
		return model.Snapshot{}, false, err
	}
	newState, stateOK := claude.ActivityForHook(hook)
	subagentDelta, deltaOK := claude.SubagentDeltaForHook(hook)
	if !stateOK && !deltaOK {
		return model.Snapshot{}, false, nil
	}
	sessionID := claude.CleanSessionID(hook.SessionID)

	snapshot, err = store.LoadSession(sessionID)
	if err != nil {
		if !os.IsNotExist(err) {
			return model.Snapshot{}, false, err
		}
		snapshot = model.Snapshot{
			SchemaVersion: model.CurrentSchemaVersion,
			CapturedAt:    now,
			Provider:      model.ProviderClaude,
			Session:       model.Session{ID: sessionID},
		}
	}
	activity := snapshot.Activity
	if stateOK {
		activity.State = newState
	}
	if deltaOK {
		activity.Subagents = max(0, activity.Subagents+subagentDelta)
	}
	activity.UpdatedAt = now
	snapshot.Activity = activity
	if err := store.Save(snapshot); err != nil {
		return model.Snapshot{}, false, err
	}
	return snapshot, true, nil
}
