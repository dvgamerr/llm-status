package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSetsUnsetVariables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("# comment\nPORT=9090\nLOG_FORMAT=text\nLOG_LEVEL=\"debug\"\n\nEMPTY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"PORT", "LOG_FORMAT", "LOG_LEVEL", "EMPTY"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}

	if err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := os.Getenv("PORT"); got != "9090" {
		t.Errorf("PORT = %q, want 9090", got)
	}
	if got := os.Getenv("LOG_FORMAT"); got != "text" {
		t.Errorf("LOG_FORMAT = %q, want text", got)
	}
	if got := os.Getenv("LOG_LEVEL"); got != "debug" {
		t.Errorf("LOG_LEVEL = %q, want debug (quotes stripped)", got)
	}
}

func TestLoadDoesNotOverrideExistingEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("LOG_LEVEL=debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LOG_LEVEL", "error")

	if err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := os.Getenv("LOG_LEVEL"); got != "error" {
		t.Errorf("LOG_LEVEL = %q, want error (real env must win)", got)
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "does-not-exist.env")); err != nil {
		t.Fatalf("Load: %v", err)
	}
}
