package state

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
)

func TestSaveAndLoadMultipleSessions(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	older := sampleSnapshot("session/with/unsafe/path", time.Unix(100, 0))
	newer := sampleSnapshot("second", time.Unix(200, 0))
	if saveErr := store.Save(older); saveErr != nil {
		t.Fatalf("Save(older) error = %v", saveErr)
	}
	if saveErr := store.Save(newer); saveErr != nil {
		t.Fatalf("Save(newer) error = %v", saveErr)
	}

	snapshots, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("LoadAll() returned %d snapshots, want 2", len(snapshots))
	}
	if snapshots[0].Session.ID != "second" || snapshots[1].Session.ID != older.Session.ID {
		t.Fatalf("snapshots are not newest-first: %+v", snapshots)
	}
	latest, err := store.LoadLatest()
	if err != nil {
		t.Fatalf("LoadLatest() error = %v", err)
	}
	if latest.Session.ID != "second" {
		t.Fatalf("latest session = %q, want second", latest.Session.ID)
	}

	entries, err := os.ReadDir(filepath.Join(store.Dir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "session") || strings.Contains(entry.Name(), "unsafe") {
			t.Fatalf("session filename leaked or trusted the ID: %q", entry.Name())
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(store.Dir(), latestFile))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("latest.json permission = %o, want 600", got)
		}
	}
}

func TestSaveOverwritesSessionAtomically(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	first := sampleSnapshot("same", time.Unix(100, 0))
	second := sampleSnapshot("same", time.Unix(200, 0))
	if saveErr := store.Save(first); saveErr != nil {
		t.Fatal(saveErr)
	}
	if saveErr := store.Save(second); saveErr != nil {
		t.Fatal(saveErr)
	}
	snapshots, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || !snapshots[0].CapturedAt.Equal(second.CapturedAt) {
		t.Fatalf("unexpected overwritten state: %+v", snapshots)
	}
	leftovers, err := filepath.Glob(filepath.Join(store.Dir(), "sessions", ".snapshot-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
}

func TestLoadAllSkipsCorruptFiles(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := store.Save(sampleSnapshot("valid", time.Now())); saveErr != nil {
		t.Fatal(saveErr)
	}
	corrupt := filepath.Join(store.Dir(), "sessions", "corrupt.json")
	if writeErr := os.WriteFile(corrupt, []byte("{"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	snapshots, err := store.LoadAll()
	if err == nil || !strings.Contains(err.Error(), "ignored 1 invalid session snapshot") {
		t.Fatalf("LoadAll() error = %v, want corruption warning", err)
	}
	if len(snapshots) != 1 || snapshots[0].Session.ID != "valid" {
		t.Fatalf("unexpected snapshots: %+v", snapshots)
	}
}

func TestSaveRejectsMissingCaptureTime(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := sampleSnapshot("missing-time", time.Time{})
	if err := store.Save(snapshot); err == nil || !strings.Contains(err.Error(), "capture time") {
		t.Fatalf("Save() error = %v, want capture time error", err)
	}
}

func TestDefaultDirHonorsEnvironment(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom")
	t.Setenv("LLM_STATUS_STATE_DIR", want)
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("DefaultDir() = %q, want %q", got, want)
	}
}

func TestLoadSessionRejectsEmptyID(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSession(" \t"); err == nil || !strings.Contains(err.Error(), "session ID") {
		t.Fatalf("LoadSession() error = %v", err)
	}
}

func TestLoadLatestRejectsOversizedSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, latestFile), bytes.Repeat([]byte("x"), maxSnapshotBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadLatest(); !errors.Is(err, errSnapshotTooLarge) {
		t.Fatalf("LoadLatest() error = %v, want size-limit error", err)
	}
}

func TestDefaultDirWithoutOverrideUsesUserCacheDir(t *testing.T) {
	t.Setenv("LLM_STATUS_STATE_DIR", "")
	cacheDir, cacheErr := os.UserCacheDir()
	if cacheErr != nil {
		t.Skipf("no user cache directory available on this platform: %v", cacheErr)
	}
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cacheDir, appDirectory)
	if got != want {
		t.Fatalf("DefaultDir() = %q, want %q", got, want)
	}
}

func TestDefaultDirTreatsBlankOverrideAsUnset(t *testing.T) {
	t.Setenv("LLM_STATUS_STATE_DIR", "   \t  ")
	cacheDir, cacheErr := os.UserCacheDir()
	if cacheErr != nil {
		t.Skipf("no user cache directory available on this platform: %v", cacheErr)
	}
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cacheDir, appDirectory)
	if got != want {
		t.Fatalf("DefaultDir() = %q, want %q (blank override should be ignored)", got, want)
	}
}

func TestDefaultDirPropagatesUserCacheDirError(t *testing.T) {
	t.Setenv("LLM_STATUS_STATE_DIR", "")
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LocalAppData", "")
	case "darwin", "ios":
		t.Setenv("HOME", "")
	case "plan9":
		t.Setenv("home", "")
	default:
		t.Setenv("XDG_CACHE_HOME", "")
		t.Setenv("HOME", "")
	}
	if _, err := DefaultDir(); err == nil {
		t.Fatal("DefaultDir() error = nil, want error when the user cache directory is unresolvable")
	}
}

func TestNewRejectsEmptyDir(t *testing.T) {
	if _, err := New("   "); err == nil || !strings.Contains(err.Error(), "state directory is empty") {
		t.Fatalf("New() error = %v, want empty-directory error", err)
	}
}

func TestEnsurePrivateDirCreatesAndReusesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("ensurePrivateDir() first call error = %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("ensurePrivateDir() did not create a directory at %q", dir)
	}
	// Second call must succeed against the already-existing directory.
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("ensurePrivateDir() second call error = %v", err)
	}
}

func TestEnsurePrivateDirFailsWhenPathIsFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocker, "state")
	if err := ensurePrivateDir(target); err == nil {
		t.Fatal("ensurePrivateDir() error = nil, want error when a path component is a regular file")
	}
}

func TestSaveRejectsUnsupportedSchemaVersion(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := sampleSnapshot("bad-schema", time.Now())
	snapshot.SchemaVersion = model.CurrentSchemaVersion + 1
	if err := store.Save(snapshot); err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("Save() error = %v, want schema version error", err)
	}
}

func TestSaveRejectsEmptySessionID(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := sampleSnapshot("  ", time.Now())
	if err := store.Save(snapshot); err == nil || !strings.Contains(err.Error(), "session ID") {
		t.Fatalf("Save() error = %v, want session ID error", err)
	}
}

func TestSaveFailsWhenStateDirIsFile(t *testing.T) {
	root := t.TempDir()
	blockedDir := filepath.Join(root, "state")
	if err := os.WriteFile(blockedDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(blockedDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sampleSnapshot("blocked", time.Now())); err == nil {
		t.Fatal("Save() error = nil, want error when the state directory path is a regular file")
	}
}

func TestSaveFailsWhenSessionsPathIsFile(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), "sessions"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sampleSnapshot("blocked", time.Now())); err == nil {
		t.Fatal("Save() error = nil, want error when the sessions directory path is a regular file")
	}
}

func TestSaveRejectsUnmarshalableSnapshot(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := sampleSnapshot("nan-cost", time.Now())
	nan := math.NaN()
	snapshot.Cost.TotalCostUSD = &nan
	if err := store.Save(snapshot); err == nil || !strings.Contains(err.Error(), "encode snapshot") {
		t.Fatalf("Save() error = %v, want encode error", err)
	}
}

func TestSaveFailsWritingSessionFileWhenTargetIsDirectory(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	sessionsDir := filepath.Join(store.Dir(), "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := sampleSnapshot("dir-collision", time.Now())
	blocked := filepath.Join(sessionsDir, sessionFilename(snapshot.Session.ID))
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(snapshot); err == nil || !strings.Contains(err.Error(), "write session snapshot") {
		t.Fatalf("Save() error = %v, want write session snapshot error", err)
	}
}

func TestSaveFailsWritingLatestFileWhenTargetIsDirectory(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.Dir(), latestFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sampleSnapshot("latest-collision", time.Now())); err == nil || !strings.Contains(err.Error(), "write latest snapshot") {
		t.Fatalf("Save() error = %v, want write latest snapshot error", err)
	}
}

func TestLoadSessionReturnsSavedSnapshot(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	want := sampleSnapshot("round-trip", time.Unix(500, 0))
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadSession("round-trip")
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got.Session.ID != want.Session.ID || !got.CapturedAt.Equal(want.CapturedAt) {
		t.Fatalf("LoadSession() = %+v, want %+v", got, want)
	}
}

func TestLoadSessionMissingReturnsNotExist(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSession("never-saved"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadSession() error = %v, want os.ErrNotExist", err)
	}
}

func TestLoadLatestMissingReturnsNotExist(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadLatest(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadLatest() error = %v, want os.ErrNotExist", err)
	}
}

func TestLoadAllReturnsNilWhenSessionsDirMissing(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v, want nil for a store with no sessions directory yet", err)
	}
	if snapshots != nil {
		t.Fatalf("LoadAll() = %+v, want nil", snapshots)
	}
}

func TestLoadAllSkipsSubdirectoriesAndNonJSONFiles(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sampleSnapshot("valid", time.Now())); err != nil {
		t.Fatal(err)
	}
	sessionsDir := filepath.Join(store.Dir(), "sessions")
	if err := os.Mkdir(filepath.Join(sessionsDir, "a-directory.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "notes.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll() error = %v, want no corruption warnings", err)
	}
	if len(snapshots) != 1 || snapshots[0].Session.ID != "valid" {
		t.Fatalf("LoadAll() = %+v, want only the valid session", snapshots)
	}
}

func TestLoadAllPropagatesReadDirError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs don't map onto Unix chmod semantics; verify this permission-denied path on Linux")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission checks")
	}
	store, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	sessionsDir := filepath.Join(store.Dir(), "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sessionsDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessionsDir, 0o700) })
	if _, err := store.LoadAll(); err == nil || !strings.Contains(err.Error(), "read sessions directory") {
		t.Fatalf("LoadAll() error = %v, want read sessions directory error", err)
	}
}

func TestReadSnapshotRejectsUnsupportedSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	raw := `{"schema_version":999,"captured_at":"2026-01-01T00:00:00Z","session":{"id":"x"}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshot(path); err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("readSnapshot() error = %v, want schema version error", err)
	}
}

func TestReadSnapshotRejectsEmptySessionID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	raw := `{"schema_version":1,"captured_at":"2026-01-01T00:00:00Z","session":{"id":""}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshot(path); err == nil || !strings.Contains(err.Error(), "no session ID") {
		t.Fatalf("readSnapshot() error = %v, want missing session ID error", err)
	}
}

func TestReadSnapshotRejectsZeroCapturedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	raw := `{"schema_version":1,"session":{"id":"x"}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshot(path); err == nil || !strings.Contains(err.Error(), "no capture time") {
		t.Fatalf("readSnapshot() error = %v, want missing capture time error", err)
	}
}

// TestReadFileWithRetryExhaustsRetriesOnPersistentError points readSnapshot
// at a directory instead of a file. Opening a directory succeeds on every
// platform, but reading from it fails immediately and keeps failing across
// every retry attempt (it is neither os.ErrNotExist nor errSnapshotTooLarge),
// so this exercises the sleep-and-retry loop in readFileWithRetry and its
// final non-transient return, cross-platform.
func TestReadFileWithRetryExhaustsRetriesOnPersistentError(t *testing.T) {
	dir := t.TempDir()
	asDir := filepath.Join(dir, "snapshot.json")
	if err := os.Mkdir(asDir, 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := readSnapshot(asDir); err == nil {
		t.Fatal("readSnapshot() error = nil, want error reading a directory as a snapshot file")
	}
	minElapsed := readRetryDelay * (readRetryAttempts - 1)
	if elapsed := time.Since(start); elapsed < minElapsed {
		t.Fatalf("readSnapshot() returned after %v, want at least %v of retry sleeps", elapsed, minElapsed)
	}
}

func sampleSnapshot(id string, capturedAt time.Time) model.Snapshot {
	return model.Snapshot{
		SchemaVersion: model.CurrentSchemaVersion,
		CapturedAt:    capturedAt.UTC(),
		Session:       model.Session{ID: id},
	}
}
