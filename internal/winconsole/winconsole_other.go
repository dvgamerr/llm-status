//go:build !windows

package winconsole

import "os/exec"

// HideServiceConsole is a no-op outside Windows: systemd and launchd run
// this binary as a plain background process with no console attached.
func HideServiceConsole() bool { return false }

// HideDetachedConsole is a no-op outside Windows, where a process launched
// by a GUI parent simply inherits no terminal instead of being handed a
// fresh window.
func HideDetachedConsole() bool { return false }

// SuppressChildConsole is a no-op outside Windows.
func SuppressChildConsole(*exec.Cmd) {}
