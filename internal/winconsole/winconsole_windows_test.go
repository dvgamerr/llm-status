//go:build windows

package winconsole

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func stubConsole(t *testing.T, handle uintptr, processCount uint32) *bool {
	t.Helper()
	hidden := false
	originalWindow, originalCount, originalHide := consoleWindow, consoleProcessCount, hideConsoleWindow
	consoleWindow = func() uintptr { return handle }
	consoleProcessCount = func() uint32 { return processCount }
	hideConsoleWindow = func(uintptr) bool {
		hidden = true
		return true
	}
	t.Cleanup(func() {
		consoleWindow, consoleProcessCount, hideConsoleWindow = originalWindow, originalCount, originalHide
	})
	return &hidden
}

func TestHideDetachedConsoleOnlyHidesAConsoleThisProcessOwnsAlone(t *testing.T) {
	for name, testCase := range map[string]struct {
		handle       uintptr
		processCount uint32
		want         bool
	}{
		"spawned by a GUI parent": {handle: 0x42, processCount: 1, want: true},
		"inherited from a shell":  {handle: 0x42, processCount: 2, want: false},
		"no console at all":       {handle: 0, processCount: 1, want: false},
		"process list failed":     {handle: 0x42, processCount: 0, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			hidden := stubConsole(t, testCase.handle, testCase.processCount)
			if got := HideDetachedConsole(); got != testCase.want || *hidden != testCase.want {
				t.Fatalf("HideDetachedConsole() = %v (hidden %v), want %v", got, *hidden, testCase.want)
			}
		})
	}
}

func TestHideServiceConsoleIgnoresTheProcessCount(t *testing.T) {
	hidden := stubConsole(t, 0x42, 3)
	if !HideServiceConsole() || !*hidden {
		t.Fatal("a service console attached to other processes was left visible")
	}

	hidden = stubConsole(t, 0, 1)
	if HideServiceConsole() || *hidden {
		t.Fatal("hid a console that does not exist")
	}
}

func TestSuppressChildConsole(t *testing.T) {
	command := exec.Command("cmd", "/c", "exit")
	SuppressChildConsole(command)
	attr := command.SysProcAttr
	if attr == nil || !attr.HideWindow || attr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("child console was not suppressed: %+v", attr)
	}

	// An existing SysProcAttr keeps whatever the caller already set.
	command = exec.Command("cmd", "/c", "exit")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	SuppressChildConsole(command)
	attr = command.SysProcAttr
	if attr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 || attr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("existing creation flags were dropped: %+v", attr)
	}

	SuppressChildConsole(nil) // must not panic
}

// The real syscalls are exercised only for their contract that they never
// panic and report "no console"/"nothing to hide" safely; a test binary run
// by `go test` may or may not own a console.
func TestRealConsoleProbesAreSafe(t *testing.T) {
	if handle := getConsoleWindow(); handle != 0 && getConsoleProcessCount() == 0 {
		t.Fatal("a console exists but reported zero attached processes")
	}
}
