//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dvgamerr/llm-status/internal/atomicfile"
)

// macOS runs the relay as a per-user LaunchAgent. `launchctl load/unload`
// (rather than the newer bootstrap/bootout) is used because it's the one
// interface that's worked unchanged across every recent macOS version —
// this package has not been exercised on real macOS hardware, so it
// deliberately avoids the newer API's version-specific edge cases.
func plistPath(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", name+".plist"), nil
}

// Install writes and loads a per-user launchd LaunchAgent.
func Install(cfg Config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	path, err := plistPath(cfg.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory for logs: %w", err)
	}
	logPath := filepath.Join(home, "Library", "Logs", cfg.Name+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	args := append([]string{exePath}, cfg.Args...)
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, cfg.Name, plistArgumentList(args), logPath, logPath)

	if err := atomicfile.Write(path, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write launchd plist: %w", err)
	}

	// Unload any previous instance first (e.g. from a prior version's
	// plist) so `load` picks up the new one instead of erroring "already
	// loaded" on a stale definition.
	_ = launchctl("unload", "-w", path)
	return launchctl("load", "-w", path)
}

// Remove unloads and deletes a launchd LaunchAgent.
func Remove(name string) error {
	path, err := plistPath(name)
	if err != nil {
		return err
	}
	_ = launchctl("unload", "-w", path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove launchd plist: %w", err)
	}
	return nil
}

// Start asks launchd to start an installed agent.
func Start(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return launchctl("start", name)
}

// Stop asks launchd to stop an installed agent.
func Stop(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return launchctl("stop", name)
}

// Status queries a launchd agent's installation and running state.
func Status(name string) (State, error) {
	path, err := plistPath(name)
	if err != nil {
		return StateNotInstalled, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return StateNotInstalled, nil
		}
		return StateNotInstalled, fmt.Errorf("inspect launchd plist: %w", err)
	}
	out, err := exec.Command("launchctl", "list", name).Output()
	if err != nil {
		return StateStopped, nil
	}
	// `launchctl list <label>` prints a key-value plist-ish block with a
	// "PID" entry only while the job is actually running.
	if strings.Contains(string(out), `"PID"`) {
		return StateRunning, nil
	}
	return StateStopped, nil
}

func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, detail)
		}
		return fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func plistArgumentList(args []string) string {
	lines := make([]string, 0, len(args))
	for _, arg := range args {
		lines = append(lines, "\t\t<string>"+plistEscape(arg)+"</string>")
	}
	return strings.Join(lines, "\n")
}

func plistEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}
