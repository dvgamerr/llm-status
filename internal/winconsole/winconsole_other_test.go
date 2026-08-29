//go:build !windows

package winconsole

import (
	"os/exec"
	"testing"
)

func TestConsoleHelpersAreNoOpsOffWindows(t *testing.T) {
	if HideServiceConsole() || HideDetachedConsole() {
		t.Fatal("a console was reported hidden on a platform without consoles")
	}
	command := exec.Command("true")
	SuppressChildConsole(command)
	if command.SysProcAttr != nil {
		t.Fatalf("child process attributes were modified: %+v", command.SysProcAttr)
	}
	SuppressChildConsole(nil) // must not panic
}
