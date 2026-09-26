//go:build linux

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeSystemctl drops an executable script named "systemctl" on disk
// that echoes stdout/stderr (if non-empty) and exits with exitCode, then
// points PATH at it — so systemctl()/systemdState()'s real exec.Command
// call can be exercised without a real systemd session.
func writeFakeSystemctl(t *testing.T, exitCode int, stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	if stdout != "" {
		fmt.Fprintf(&body, "echo '%s'\n", stdout)
	}
	if stderr != "" {
		fmt.Fprintf(&body, "echo '%s' 1>&2\n", stderr)
	}
	fmt.Fprintf(&body, "exit %d\n", exitCode)
	path := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(path, []byte(body.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestSystemctlRealExecSuccess(t *testing.T) {
	writeFakeSystemctl(t, 0, "ok", "")
	if err := systemctl("daemon-reload"); err != nil {
		t.Fatalf("systemctl() error = %v", err)
	}
}

func TestSystemctlRealExecFailureWithOutput(t *testing.T) {
	writeFakeSystemctl(t, 1, "", "permission denied")
	err := systemctl("enable", "--now", "llm-status-test")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("systemctl() error = %v, want it to mention the command output", err)
	}
	if !strings.Contains(err.Error(), "--user enable --now llm-status-test") {
		t.Fatalf("systemctl() error = %v, want it to mention the --user argv", err)
	}
}

func TestSystemctlRealExecFailureNoOutput(t *testing.T) {
	writeFakeSystemctl(t, 1, "", "")
	if err := systemctl("stop", "llm-status-test"); err == nil {
		t.Fatal("systemctl() error = nil, want non-nil")
	}
}

func TestSystemdStateRealExec(t *testing.T) {
	writeFakeSystemctl(t, 0, "active", "")
	state, err := systemdState("llm-status-test")
	if err != nil {
		t.Fatalf("systemdState() error = %v", err)
	}
	if state != "active" {
		t.Fatalf("systemdState() = %q, want %q", state, "active")
	}
}

func TestSystemdStateRealExecFailure(t *testing.T) {
	writeFakeSystemctl(t, 3, "failed", "")
	state, err := systemdState("llm-status-test")
	if err == nil {
		t.Fatal("systemdState() error = nil, want non-nil")
	}
	if state != "failed" {
		t.Fatalf("systemdState() = %q, want %q even on error (stdout is still parsed)", state, "failed")
	}
}

func useLinuxServiceFakes(t *testing.T, home, executable string, systemctl func(...string) error) {
	t.Helper()
	oldHome := userHomeDirectory
	oldExecutable := executablePath
	oldSystemctl := runSystemctl
	userHomeDirectory = func() (string, error) { return home, nil }
	executablePath = func() (string, error) { return executable, nil }
	runSystemctl = systemctl
	t.Cleanup(func() {
		userHomeDirectory = oldHome
		executablePath = oldExecutable
		runSystemctl = oldSystemctl
	})
}

func TestLinuxInstallFormatsAndWritesSafeUnit(t *testing.T) {
	home := t.TempDir()
	var calls []string
	useLinuxServiceFakes(t, home, "/opt/status app/claude%$status", func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	})
	cfg := Config{Name: "llm-status-test", Description: "Status 100%", Args: []string{"relay", "$HOME", "a'b"}}
	if err := Install(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".config", "systemd", "user", cfg.Name+".service")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(data)
	for _, want := range []string{`Description="Status 100%%"`, `Type=exec`, `"/opt/status app/claude%%$$status" "relay" "$$HOME" "a'b"`} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit does not contain %q:\n%s", want, unit)
		}
	}
	if strings.Join(calls, "|") != "daemon-reload|enable --now llm-status-test" {
		t.Fatalf("systemctl calls = %q", calls)
	}
}

func TestLinuxRemoveStartAndStop(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "systemd", "user", "llm-status-test.service")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	useLinuxServiceFakes(t, home, "/bin/status", func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	})
	if err := Start("llm-status-test"); err != nil {
		t.Fatal(err)
	}
	if err := Stop("llm-status-test"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("llm-status-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still exists: %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("systemctl calls = %q", calls)
	}
}

func TestLinuxStatus(t *testing.T) {
	home := t.TempDir()
	useLinuxServiceFakes(t, home, "/bin/status", func(...string) error { return nil })
	if state, err := Status("llm-status-test"); err != nil || state != StateNotInstalled {
		t.Fatalf("missing Status() = %v, %v", state, err)
	}
	path, err := unitPath("llm-status-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldQuery := querySystemdState
	defer func() { querySystemdState = oldQuery }()
	querySystemdState = func(string) (string, error) { return "active", nil }
	if state, err := Status("llm-status-test"); err != nil || state != StateRunning {
		t.Fatalf("active Status() = %v, %v", state, err)
	}
	querySystemdState = func(string) (string, error) { return "failed", errors.New("exit 3") }
	if state, err := Status("llm-status-test"); err != nil || state != StateStopped {
		t.Fatalf("failed Status() = %v, %v", state, err)
	}
	querySystemdState = func(string) (string, error) { return "transport error", errors.New("exit 1") }
	if _, err := Status("llm-status-test"); err == nil {
		t.Fatal("expected unknown status error")
	}
}

func TestInactiveSystemdStates(t *testing.T) {
	for _, state := range []string{"inactive", "failed", "activating", "deactivating", "reloading", "maintenance"} {
		if !isInactiveSystemdState(state) {
			t.Fatalf("%q was not inactive", state)
		}
	}
	if isInactiveSystemdState("active") || isInactiveSystemdState("unknown") {
		t.Fatal("active or unknown state classified as inactive")
	}
}
