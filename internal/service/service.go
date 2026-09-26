// Package service installs llm-status's relay as a background service
// using whatever the host OS's own service manager is: a real Windows
// Service on Windows, a systemd --user unit on Linux, and a launchd
// LaunchAgent on macOS. One Config and one set of Install/Remove/Start/
// Stop/Status calls cover all three; the platform-specific mechanics live
// in the manager_<os>.go files, matching this repo's existing per-OS
// pattern (see internal/touch).
package service

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Config describes the background command the OS service manager should
// supervise. Args are passed to this same executable (os.Executable()) —
// e.g. {"relay", "--mirror-ssh", "pilab"} — so the installed service runs
// exactly the CLI command a user could run in a terminal themselves.
type Config struct {
	Name        string
	DisplayName string
	Description string
	Args        []string
}

// State is the coarse status of a previously installed service.
type State int

const (
	// StateNotInstalled means no service registration exists.
	StateNotInstalled State = iota
	// StateStopped means the service exists but is not running.
	StateStopped
	// StateRunning means the service exists and is running.
	StateRunning
)

func (s State) String() string {
	switch s {
	case StateNotInstalled:
		return "not installed"
	case StateRunning:
		return "running"
	case StateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// ErrUnsupportedPlatform is returned by manager_other.go on any OS this
// package doesn't yet implement a service manager for.
var ErrUnsupportedPlatform = errors.New("service management is not supported on this platform")

func validateConfig(cfg Config) error {
	if err := validateName(cfg.Name); err != nil {
		return err
	}
	fields := []struct {
		name  string
		value string
	}{
		{"display name", cfg.DisplayName},
		{"description", cfg.Description},
	}
	for _, field := range fields {
		if err := rejectControlCharacters(field.name, field.value); err != nil {
			return err
		}
	}
	for index, arg := range cfg.Args {
		if err := rejectControlCharacters(fmt.Sprintf("argument %d", index), arg); err != nil {
			return err
		}
	}
	return nil
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("service name is empty")
	}
	if len(name) > 200 {
		return errors.New("service name is longer than 200 bytes")
	}
	for _, char := range name {
		if !(unicode.IsLetter(char) || unicode.IsDigit(char) || strings.ContainsRune("_.@-", char)) {
			return fmt.Errorf("service name contains unsupported character %q", char)
		}
	}
	return nil
}

func rejectControlCharacters(label, value string) error {
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("service %s contains control character %U", label, char)
		}
	}
	return nil
}
