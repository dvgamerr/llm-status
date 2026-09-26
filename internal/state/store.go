// Package state persists and validates sanitized provider snapshots.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dvgamerr/llm-status/internal/atomicfile"
	"github.com/dvgamerr/llm-status/internal/model"
)

const (
	appDirectory = "llm-status"
	latestFile   = "latest.json"

	// A reader can land mid-rename on Windows: writeAtomic's os.Rename onto
	// an existing destination briefly holds it exclusively, and a read
	// during that window fails with "The process cannot access the file
	// because it is being used by another process" instead of a real
	// error. The write finishes in microseconds, so a few short retries
	// clear it without the relay ever having to treat it as corruption.
	readRetryAttempts = 5
	readRetryDelay    = 20 * time.Millisecond
)

// Store owns one private snapshot directory.
type Store struct {
	dir string
}

// DefaultDir resolves the configured or platform-default state directory.
func DefaultDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("LLM_STATUS_STATE_DIR")); override != "" {
		return filepath.Clean(override), nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache directory: %w", err)
	}
	return filepath.Join(cacheDir, appDirectory), nil
}

// New validates and normalizes a snapshot directory.
func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("state directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	return &Store{dir: filepath.Clean(abs)}, nil
}

// Dir returns the normalized absolute state directory.
func (s *Store) Dir() string {
	return s.dir
}

// Save atomically writes both the session-specific and latest snapshots.
func (s *Store) Save(snapshot model.Snapshot) error {
	if snapshot.SchemaVersion != model.CurrentSchemaVersion {
		return fmt.Errorf("unsupported snapshot schema version %d", snapshot.SchemaVersion)
	}
	if strings.TrimSpace(snapshot.Session.ID) == "" {
		return errors.New("snapshot session ID is empty")
	}
	if snapshot.CapturedAt.IsZero() {
		return errors.New("snapshot capture time is empty")
	}

	sessionsDir := filepath.Join(s.dir, "sessions")
	if err := ensurePrivateDir(s.dir); err != nil {
		return err
	}
	if err := ensurePrivateDir(sessionsDir); err != nil {
		return err
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	data = append(data, '\n')

	if err := writeAtomic(sessionsDir, sessionFilename(snapshot.Session.ID), data); err != nil {
		return fmt.Errorf("write session snapshot: %w", err)
	}
	if err := writeAtomic(s.dir, latestFile, data); err != nil {
		return fmt.Errorf("write latest snapshot: %w", err)
	}
	return nil
}

// LoadLatest reads and validates the most recently saved snapshot.
func (s *Store) LoadLatest() (model.Snapshot, error) {
	return readSnapshot(filepath.Join(s.dir, latestFile))
}

// LoadSession loads the most recently saved snapshot for one session ID. It
// returns an error satisfying errors.Is(err, os.ErrNotExist) when no
// snapshot has been saved for that session yet.
func (s *Store) LoadSession(sessionID string) (model.Snapshot, error) {
	if strings.TrimSpace(sessionID) == "" {
		return model.Snapshot{}, errors.New("session ID is empty")
	}
	return readSnapshot(filepath.Join(s.dir, "sessions", sessionFilename(sessionID)))
}

// LoadAll returns valid session snapshots newest-first and reports corruption.
func (s *Store) LoadAll() ([]model.Snapshot, error) {
	dir := filepath.Join(s.dir, "sessions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions directory: %w", err)
	}

	snapshots := make([]model.Snapshot, 0, len(entries))
	invalidCount := 0
	var firstInvalid error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		snapshot, err := readSnapshot(filepath.Join(dir, entry.Name()))
		if err != nil {
			// Preserve valid sessions, but surface corruption so the TUI can warn
			// instead of silently pretending the missing session never existed.
			invalidCount++
			if firstInvalid == nil {
				firstInvalid = err
			}
			continue
		}
		snapshots = append(snapshots, snapshot)
	}

	sort.SliceStable(snapshots, func(i, j int) bool {
		return snapshots[i].CapturedAt.After(snapshots[j].CapturedAt)
	})
	if invalidCount > 0 {
		return snapshots, fmt.Errorf("ignored %d invalid session snapshot(s); first error: %w", invalidCount, firstInvalid)
	}
	return snapshots, nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create state directory %q: %w", path, err)
	}
	// #nosec G302 -- directories require execute permission; access is owner-only.
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure state directory %q: %w", path, err)
	}
	return nil
}

func writeAtomic(dir, name string, data []byte) error {
	return atomicfile.Write(filepath.Join(dir, name), data, 0o600)
}

// readFileWithRetry retries a transient read failure (a file mid-rename)
// but not os.ErrNotExist, which is either a real "no such session" or a
// race with a caller-visible directory listing and shouldn't be masked by
// waiting.
func readFileWithRetry(path string) ([]byte, error) {
	var data []byte
	var err error
	for attempt := range readRetryAttempts {
		data, err = readLimitedFile(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return data, err
		}
		if errors.Is(err, errSnapshotTooLarge) {
			return nil, err
		}
		if attempt+1 < readRetryAttempts {
			time.Sleep(readRetryDelay)
		}
	}
	if isTransientReadErr(err) {
		err = fmt.Errorf("%w: %w", err, ErrTransientRead)
	}
	return data, err
}

var errSnapshotTooLarge = errors.New("snapshot exceeds size limit")

func readLimitedFile(path string) (data []byte, returnErr error) {
	// #nosec G304 -- callers construct path beneath the Store's normalized root.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close snapshot: %w", closeErr))
		}
	}()
	data, err = io.ReadAll(io.LimitReader(file, maxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSnapshotBytes {
		return nil, fmt.Errorf("%w: %q is larger than %d bytes", errSnapshotTooLarge, path, maxSnapshotBytes)
	}
	return data, nil
}

func readSnapshot(path string) (model.Snapshot, error) {
	var snapshot model.Snapshot
	data, err := readFileWithRetry(path)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, fmt.Errorf("decode %q: %w", path, err)
	}
	if snapshot.SchemaVersion != model.CurrentSchemaVersion {
		return snapshot, fmt.Errorf("unsupported snapshot schema version %d", snapshot.SchemaVersion)
	}
	if strings.TrimSpace(snapshot.Session.ID) == "" {
		return snapshot, errors.New("snapshot has no session ID")
	}
	if snapshot.CapturedAt.IsZero() {
		return snapshot, errors.New("snapshot has no capture time")
	}
	return snapshot, nil
}

func sessionFilename(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:12]) + ".json"
}
