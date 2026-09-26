// Package ingest converts Claude statusLine input into sanitized local state.
package ingest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dvgamerr/llm-status/internal/claude"
	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/state"
)

// Run decodes, persists, and formats one Claude statusLine event.
func Run(input io.Reader, output io.Writer, store *state.Store, now time.Time) (model.Snapshot, error) {
	if input == nil {
		return model.Snapshot{}, errors.New("statusLine input is nil")
	}
	if output == nil {
		return model.Snapshot{}, errors.New("status line output is nil")
	}
	if store == nil {
		return model.Snapshot{}, errors.New("snapshot store is nil")
	}
	payload, err := claude.Decode(input)
	if err != nil {
		return model.Snapshot{}, err
	}
	snapshot := claude.ToSnapshot(payload, now)
	// The statusLine refresh knows nothing about hook-driven activity state;
	// carry over whatever was last recorded for this session so a fresh
	// statusLine event doesn't erase working/idle/waiting-approval.
	if previous, err := store.LoadSession(snapshot.Session.ID); err == nil {
		snapshot.Activity = previous.Activity
	} else if !errors.Is(err, os.ErrNotExist) {
		return model.Snapshot{}, fmt.Errorf("load prior activity: %w", err)
	}
	if err := store.Save(snapshot); err != nil {
		return model.Snapshot{}, err
	}
	if _, err := fmt.Fprintln(output, claude.StatusLine(snapshot)); err != nil {
		return model.Snapshot{}, fmt.Errorf("write status line: %w", err)
	}
	return snapshot, nil
}
