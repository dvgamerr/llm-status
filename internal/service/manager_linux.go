//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dvgamerr/llm-status/internal/atomicfile"
	"github.com/dvgamerr/llm-status/internal/systemdunit"
)

var (
	userHomeDirectory = os.UserHomeDir
	executablePath    = os.Executable
	runSystemctl      = systemctl
	querySystemdState = systemdState
)

// Linux runs the relay as a systemd --user unit rather than a system-level
// one: it needs no root, starts in the same session as the desktop/SSH
// login that's actually running Claude Code, and "systemctl --user" is
// available on every systemd distribution this project targets.
func unitPath(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	home, err := userHomeDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", name+".service"), nil
}

// Install writes, enables, and starts a systemd user service.
func Install(cfg Config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	exePath, err := executablePath()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	path, err := unitPath(cfg.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}

	execStart, err := systemdunit.Command(append([]string{exePath}, cfg.Args...)...)
	if err != nil {
		return fmt.Errorf("format systemd command: %w", err)
	}
	description, err := systemdunit.Quote(cfg.Description)
	if err != nil {
		return fmt.Errorf("format systemd description: %w", err)
	}
	unit := fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
ExecStart=%s
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, description, execStart)

	if err := atomicfile.Write(path, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	if err := runSystemctl("daemon-reload"); err != nil {
		return err
	}
	return runSystemctl("enable", "--now", cfg.Name)
}

// Remove disables and deletes a systemd user service.
func Remove(name string) error {
	path, err := unitPath(name)
	if err != nil {
		return err
	}
	// Ignore "already stopped/disabled" failures — the goal is an absent
	// unit either way, and the file removal below is what actually matters.
	_ = runSystemctl("disable", "--now", name)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove systemd unit: %w", err)
	}
	return runSystemctl("daemon-reload")
}

// Start starts an installed systemd user service.
func Start(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return runSystemctl("start", name)
}

// Stop stops an installed systemd user service.
func Stop(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return runSystemctl("stop", name)
}

// Status queries a systemd user service's installation and active state.
func Status(name string) (State, error) {
	path, err := unitPath(name)
	if err != nil {
		return StateNotInstalled, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return StateNotInstalled, nil
		}
		return StateNotInstalled, fmt.Errorf("inspect systemd unit: %w", err)
	}
	active, err := querySystemdState(name)
	if active == "active" {
		return StateRunning, nil
	}
	if isInactiveSystemdState(active) {
		return StateStopped, nil
	}
	if err != nil {
		return StateStopped, fmt.Errorf("query systemd unit status: %w: %s", err, active)
	}
	return StateStopped, fmt.Errorf("systemd returned unknown unit state %q", active)
}

func systemdState(name string) (string, error) {
	out, err := exec.Command("systemctl", "--user", "is-active", name).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, detail)
		}
		return fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func isInactiveSystemdState(state string) bool {
	switch state {
	case "inactive", "failed", "activating", "deactivating", "reloading", "maintenance":
		return true
	default:
		return false
	}
}
