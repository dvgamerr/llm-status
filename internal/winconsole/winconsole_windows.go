//go:build windows

package winconsole

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const swHide = 0

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procGetConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procShowWindow            = user32.NewProc("ShowWindow")
)

// Indirection so the decision logic can be tested without a real console.
var (
	consoleWindow       = getConsoleWindow
	consoleProcessCount = getConsoleProcessCount
	hideConsoleWindow   = showWindowHidden
)

// HideServiceConsole hides this process's console window unconditionally.
// It is only ever called once the Service Control Manager has confirmed
// this is a service process: a service has no interactive user to read a
// console, so whatever window SCM handed it is pure noise.
func HideServiceConsole() bool { return hide(true) }

// HideDetachedConsole hides this process's console window only when this
// process is the sole process attached to it, which means Windows allocated
// the console for this launch rather than the process inheriting a shell's.
// That is the case for a Claude Code hook fired by the VS Code extension
// host and for an Explorer double-click; it is never the case for a command
// typed into PowerShell, whose console stays visible.
func HideDetachedConsole() bool { return hide(false) }

// SuppressChildConsole stops a child process from being given a console
// window of its own. The relay's ssh invocations are the reason this
// exists: they run unattended, several times a minute, and their output is
// already captured through pipes.
func SuppressChildConsole(command *exec.Cmd) {
	if command == nil {
		return
	}
	attr := command.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
		command.SysProcAttr = attr
	}
	attr.HideWindow = true
	attr.CreationFlags |= windows.CREATE_NO_WINDOW
}

func hide(unconditional bool) bool {
	handle := consoleWindow()
	if handle == 0 {
		return false
	}
	if !unconditional && consoleProcessCount() != 1 {
		return false
	}
	return hideConsoleWindow(handle)
}

func getConsoleWindow() uintptr {
	handle, _, _ := procGetConsoleWindow.Call()
	return handle
}

// getConsoleProcessCount reports how many processes share this console.
// GetConsoleProcessList returns the total count even when the supplied
// buffer is too small, so a two-slot buffer is enough to tell "only us"
// apart from "shared with a shell". A zero return means the call failed;
// reporting 0 keeps the caller from hiding a console it cannot reason about.
func getConsoleProcessCount() uint32 {
	var pids [2]uint32
	count, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return uint32(count)
}

func showWindowHidden(handle uintptr) bool {
	_, _, _ = procShowWindow.Call(handle, swHide)
	// ShowWindow's return value reports the window's previous visibility,
	// not success, so there is nothing meaningful to propagate: reaching
	// here means the hide request was issued.
	return true
}
