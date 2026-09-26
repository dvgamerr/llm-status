// Package winconsole keeps Windows console windows out of the user's face.
//
// llm-status.exe is a console-subsystem binary, which is what makes it
// usable from a terminal (and what lets install-windows.ps1 wait on
// `service install` and see its exit code). The cost is that Windows
// allocates a brand-new console window whenever a process with no console
// of its own launches it — the Service Control Manager starting the relay,
// or the VS Code extension host firing an `activity` hook. Nobody ever
// reads those consoles; they only flash on screen.
//
// The helpers here suppress exactly those cases and deliberately leave a
// console the process inherited from a real shell alone, so running
// `llm-status service install` from PowerShell still prints where the
// operator typed it. Every function is a no-op outside Windows.
package winconsole
