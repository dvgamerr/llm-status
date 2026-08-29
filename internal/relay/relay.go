// Package relay mirrors locally persisted snapshots from short-lived source
// commands to a remote dashboard. Keeping transport in a long-lived process
// means Claude statusLine and hook commands never have to wait for SSH.
package relay

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/rs/zerolog"

	"github.com/dvgamerr/claude-status/internal/model"
	"github.com/dvgamerr/claude-status/internal/state"
)

// Sender delivers one sanitized snapshot.
type Sender func(context.Context, model.Snapshot) error

// Relay retries changed provider snapshots and remembers successful deliveries.
type Relay struct {
	store         *state.Store
	send          Sender
	logger        zerolog.Logger
	sent          map[string][sha256.Size]byte
	failures      map[string]string
	lastLoadError string
}

// New validates dependencies and constructs a relay worker.
func New(store *state.Store, send Sender, logger zerolog.Logger) (*Relay, error) {
	if store == nil {
		return nil, errors.New("snapshot store is nil")
	}
	if send == nil {
		return nil, errors.New("snapshot sender is nil")
	}
	return &Relay{
		store:    store,
		send:     send,
		logger:   logger,
		sent:     make(map[string][sha256.Size]byte),
		failures: make(map[string]string),
	}, nil
}

// Sync sends the newest locally persisted snapshot for every provider whose
// sanitized content has changed since its last successful delivery. Failed
// deliveries stay pending and are retried by the next call.
func (r *Relay) Sync(ctx context.Context) error {
	snapshots, loadErr := r.store.LoadAll()
	r.logLoadError(loadErr)

	selected := latestProviders(snapshots)
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].CapturedAt.Equal(selected[j].CapturedAt) {
			return providerKey(selected[i]) < providerKey(selected[j])
		}
		return selected[i].CapturedAt.Before(selected[j].CapturedAt)
	})

	var syncErrors []error
	if loadErr != nil {
		syncErrors = append(syncErrors, loadErr)
	}
	for _, snapshot := range selected {
		key := providerKey(snapshot)
		fingerprint, err := snapshotFingerprint(snapshot)
		if err != nil {
			syncErrors = append(syncErrors, err)
			continue
		}
		if delivered, ok := r.sent[key]; ok && delivered == fingerprint {
			continue
		}

		if err := r.send(ctx, snapshot); err != nil {
			wrapped := fmt.Errorf("mirror %s snapshot: %w", key, err)
			r.logFailure(key, wrapped)
			syncErrors = append(syncErrors, wrapped)
			continue
		}
		_, alreadyDelivered := r.sent[key]
		r.sent[key] = fingerprint
		if _, recovering := r.failures[key]; recovering {
			r.logger.Info().Str("provider", key).Time("captured_at", snapshot.CapturedAt).Msg("mirror recovered")
		} else if !alreadyDelivered {
			r.logger.Info().Str("provider", key).Time("captured_at", snapshot.CapturedAt).Msg("mirrored snapshot")
		}
		delete(r.failures, key)
	}
	return errors.Join(syncErrors...)
}

// logLoadError logs a change in the local snapshot store's load health, and
// nothing on repeat failures/recoveries so a stuck condition doesn't spam
// one line per Sync tick. A read race clearing mid-rename (state.
// ErrTransientRead) logs at Debug rather than Warn: it always resolves on
// the very next Sync and isn't actionable the way real corruption is.
func (r *Relay) logLoadError(err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	if detail == r.lastLoadError {
		return
	}
	r.lastLoadError = detail
	if err == nil {
		r.logger.Info().Msg("snapshot store recovered")
		return
	}
	event := r.logger.Warn()
	if errors.Is(err, state.ErrTransientRead) {
		event = r.logger.Debug()
	}
	event.Err(err).Msg("load snapshots")
}

func (r *Relay) logFailure(key string, err error) {
	detail := err.Error()
	if r.failures[key] == detail {
		return
	}
	r.failures[key] = detail
	r.logger.Warn().Str("provider", key).Err(err).Msg("mirror failed")
}

func latestProviders(snapshots []model.Snapshot) []model.Snapshot {
	groups := make(map[string][]model.Snapshot)
	for _, snapshot := range snapshots {
		key := providerKey(snapshot)
		groups[key] = append(groups[key], snapshot)
	}
	selected := make([]model.Snapshot, 0, len(groups))
	for _, group := range groups {
		// Newest snapshot per provider, with any usage fields it lacks
		// backfilled — a VS Code session only ever writes activity, so
		// without this the Pi would receive a snapshot of nothing but a
		// mascot state and blank every number on the dashboard.
		if snapshot, ok := model.LatestWithUsage(group); ok {
			selected = append(selected, snapshot)
		}
	}
	return selected
}

func providerKey(snapshot model.Snapshot) string {
	provider := model.CanonicalProvider(snapshot.Provider)
	if provider != "" {
		return provider
	}
	return "session:" + snapshot.Session.ID
}

func snapshotFingerprint(snapshot model.Snapshot) ([sha256.Size]byte, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode %s snapshot: %w", providerKey(snapshot), err)
	}
	return sha256.Sum256(data), nil
}
