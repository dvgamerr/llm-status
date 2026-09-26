package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// currentTestUser returns an account name that user.Lookup can resolve on
// whatever OS the test runs on — "root" only exists on Unix-likes, so the
// deeper root-owned branches of runPiInstall use the actual test-runner
// account instead of assuming one specific name is universal.
func currentTestUser(t *testing.T) string {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Skipf("user.Current() unavailable: %v", err)
	}
	name := current.Username
	if idx := strings.LastIndexByte(name, '\\'); idx >= 0 {
		name = name[idx+1:] // strip a Windows DOMAIN\ prefix
	}
	return name
}

// writeFakeExecutable drops a script named name on disk that this OS's
// exec.Command("name", ...) will resolve and run: a .bat on Windows (found
// via PATHEXT), a plain shebang script elsewhere. It prints stdout/stderr
// (if non-empty) and exits with exitCode, so tests can drive
// reExecWithSudo/runSystemCommand's real exec.Command call without ever
// touching a real sudo/systemctl binary.
func writeFakeExecutable(t *testing.T, dir, name string, exitCode int, stdout, stderr string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		var body strings.Builder
		body.WriteString("@echo off\r\n")
		if stdout != "" {
			fmt.Fprintf(&body, "echo %s\r\n", stdout)
		}
		if stderr != "" {
			fmt.Fprintf(&body, "echo %s 1>&2\r\n", stderr)
		}
		fmt.Fprintf(&body, "exit /b %d\r\n", exitCode)
		if err := os.WriteFile(filepath.Join(dir, name+".bat"), []byte(body.String()), 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	if stdout != "" {
		fmt.Fprintf(&body, "echo '%s'\n", stdout)
	}
	if stderr != "" {
		fmt.Fprintf(&body, "echo '%s' 1>&2\n", stderr)
	}
	fmt.Fprintf(&body, "exit %d\n", exitCode)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body.String()), 0o755); err != nil {
		t.Fatal(err)
	}
}

// isolatePath restricts PATH to exactly the given directories (in order),
// so a fake executable is guaranteed to win over (or entirely replace) any
// real binary of the same name on the host.
func isolatePath(t *testing.T, dirs ...string) {
	t.Helper()
	t.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

func TestRunSystemCommandSuccess(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "fakecmd", 0, "did the thing", "")
	isolatePath(t, dir)
	if err := runSystemCommand("fakecmd", "arg1", "arg2"); err != nil {
		t.Fatalf("runSystemCommand() error = %v", err)
	}
}

func TestRunSystemCommandFailureWithOutput(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "fakecmd", 1, "", "permission denied")
	isolatePath(t, dir)
	err := runSystemCommand("fakecmd", "enable", "--now")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("runSystemCommand() error = %v, want it to mention the command output", err)
	}
}

func TestRunSystemCommandFailureNoOutput(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "fakecmd", 1, "", "")
	isolatePath(t, dir)
	if err := runSystemCommand("fakecmd"); err == nil {
		t.Fatal("runSystemCommand() error = nil, want non-nil")
	}
}

func TestRunSystemCommandNotFound(t *testing.T) {
	isolatePath(t, t.TempDir())
	if err := runSystemCommand("this-binary-does-not-exist-anywhere"); err == nil {
		t.Fatal("runSystemCommand() error = nil, want a lookup error")
	}
}

func TestReExecWithSudoSuccess(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "sudo", 0, "installed", "")
	isolatePath(t, dir)
	var stderr bytes.Buffer
	if exitCode := reExecWithSudo("/opt/llm-status", []string{"--user", "root"}, &stderr); exitCode != 0 {
		t.Fatalf("reExecWithSudo() = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestReExecWithSudoPropagatesExitCode(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, "sudo", 7, "", "denied")
	isolatePath(t, dir)
	var stderr bytes.Buffer
	if exitCode := reExecWithSudo("/opt/llm-status", nil, &stderr); exitCode != 7 {
		t.Fatalf("reExecWithSudo() = %d, want 7", exitCode)
	}
}

func TestReExecWithSudoCommandNotFound(t *testing.T) {
	isolatePath(t, t.TempDir())
	var stderr bytes.Buffer
	if exitCode := reExecWithSudo("/opt/llm-status", nil, &stderr); exitCode != 1 {
		t.Fatalf("reExecWithSudo() = %d, want 1 (sudo not found), stderr = %q", exitCode, stderr.String())
	}
}

// failingWriter always errors, to exercise the "stdout/stderr write itself
// failed" branches that a normal bytes.Buffer can never reach.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRunPiUsageWriteFailure(t *testing.T) {
	if exitCode := runPi(nil, &bytes.Buffer{}, failingWriter{}); exitCode != 1 {
		t.Fatalf("runPi(nil) with failing stderr = %d, want 1", exitCode)
	}
}

func TestRunPiHelpWriteFailure(t *testing.T) {
	if exitCode := runPi([]string{"help"}, failingWriter{}, &bytes.Buffer{}); exitCode != 1 {
		t.Fatalf("runPi(help) with failing stdout = %d, want 1", exitCode)
	}
}

func TestRunPiUsageAndSubcommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := runPi(nil, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("runPi(nil) = %d, want 2", exitCode)
	}

	stdout.Reset()
	if exitCode := runPi([]string{"help"}, &stdout, &stderr); exitCode != 0 || !strings.Contains(stdout.String(), "Usage") {
		t.Fatalf("runPi(help) = %d, stdout = %q", exitCode, stdout.String())
	}

	stderr.Reset()
	if exitCode := runPi([]string{"bogus"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("runPi(bogus) = %d, want 2", exitCode)
	}
}

func TestRunPiInstallRejectsBadFlags(t *testing.T) {
	var stderr bytes.Buffer
	if exitCode := runPiInstall([]string{"--refresh", "1ms"}, io.Discard, &stderr); exitCode != 2 {
		t.Fatalf("runPiInstall(bad refresh) = %d, want 2", exitCode)
	}
}

func TestRunPiInstallNonLinuxGOOS(t *testing.T) {
	old := goos
	defer func() { goos = old }()
	goos = "plan9"

	var stdout, stderr bytes.Buffer
	if exitCode := runPiInstall(nil, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("runPiInstall() on fake non-linux GOOS = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "Linux") {
		t.Fatalf("stderr = %q, want a Linux-only complaint", stderr.String())
	}
}

func TestRunPiInstallReExecsAsSudoWhenNotRoot(t *testing.T) {
	old := goos
	defer func() { goos = old }()
	goos = "linux"

	oldEuid := geteuid
	defer func() { geteuid = oldEuid }()
	geteuid = func() int { return 501 }

	dir := t.TempDir()
	writeFakeExecutable(t, dir, "sudo", 3, "", "")
	isolatePath(t, dir)

	var stdout, stderr bytes.Buffer
	if exitCode := runPiInstall(nil, &stdout, &stderr); exitCode != 3 {
		t.Fatalf("runPiInstall() as non-root = %d, want 3 (propagated from fake sudo)", exitCode)
	}
}

func TestRunPiInstallUnknownUser(t *testing.T) {
	old := goos
	defer func() { goos = old }()
	goos = "linux"

	oldEuid := geteuid
	defer func() { geteuid = oldEuid }()
	geteuid = func() int { return 0 }

	var stdout, stderr bytes.Buffer
	exitCode := runPiInstall([]string{"--user", "no-such-user-should-exist-1234567"}, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("runPiInstall() unknown user = %d, want 1", exitCode)
	}
}

func TestRunPiInstallWriteUnitFails(t *testing.T) {
	old := goos
	defer func() { goos = old }()
	goos = "linux"

	oldEuid := geteuid
	defer func() { geteuid = oldEuid }()
	geteuid = func() int { return 0 }

	oldUnitDir := systemdUnitDir
	defer func() { systemdUnitDir = oldUnitDir }()
	// Parent directory doesn't exist, so atomicfile.Write's os.CreateTemp
	// fails deterministically without needing real filesystem permissions.
	systemdUnitDir = filepath.Join(t.TempDir(), "does", "not", "exist")

	var stdout, stderr bytes.Buffer
	exitCode := runPiInstall([]string{"--user", currentTestUser(t)}, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("runPiInstall() with unwritable unit dir = %d, want 1, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunPiInstallSystemctlDaemonReloadFails(t *testing.T) {
	old := goos
	defer func() { goos = old }()
	goos = "linux"

	oldEuid := geteuid
	defer func() { geteuid = oldEuid }()
	geteuid = func() int { return 0 }

	oldUnitDir := systemdUnitDir
	defer func() { systemdUnitDir = oldUnitDir }()
	systemdUnitDir = t.TempDir()

	binDir := t.TempDir()
	writeFakeExecutable(t, binDir, "systemctl", 1, "", "daemon-reload broke")
	isolatePath(t, binDir)

	var stdout, stderr bytes.Buffer
	exitCode := runPiInstall([]string{"--user", currentTestUser(t)}, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("runPiInstall() with failing systemctl = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "daemon-reload") {
		t.Fatalf("stderr = %q, want it to mention daemon-reload", stderr.String())
	}
}

func TestRunPiInstallSystemctlEnableFails(t *testing.T) {
	old := goos
	defer func() { goos = old }()
	goos = "linux"

	oldEuid := geteuid
	defer func() { geteuid = oldEuid }()
	geteuid = func() int { return 0 }

	oldUnitDir := systemdUnitDir
	defer func() { systemdUnitDir = oldUnitDir }()
	systemdUnitDir = t.TempDir()

	// Succeeds for "daemon-reload" but fails for "enable", so the failure
	// is attributable specifically to the second runSystemCommand call.
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\ndaemon-reload) exit 0 ;;\n*) echo 'enable broke' 1>&2; exit 1 ;;\nesac\n"
	if runtime.GOOS == "windows" {
		script = "@echo off\r\nif \"%1\"==\"daemon-reload\" exit /b 0\r\necho enable broke 1>&2\r\nexit /b 1\r\n"
		if err := os.WriteFile(filepath.Join(binDir, "systemctl.bat"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	isolatePath(t, binDir)

	var stdout, stderr bytes.Buffer
	exitCode := runPiInstall([]string{"--user", currentTestUser(t)}, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("runPiInstall() with failing enable = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "enable") {
		t.Fatalf("stderr = %q, want it to mention systemctl enable", stderr.String())
	}
}

func TestRunPiDispatchesToInstall(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// Bad flags make runPiInstall return quickly (exit 2) without needing
	// the full sudo/systemctl seam setup — this just proves runPi's
	// "install" case actually reaches runPiInstall.
	if exitCode := runPi([]string{"install", "--refresh", "1ms"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("runPi(install ...) = %d, want 2", exitCode)
	}
}

func TestRunPiInstallSucceedsAsRoot(t *testing.T) {
	old := goos
	defer func() { goos = old }()
	goos = "linux"

	oldEuid := geteuid
	defer func() { geteuid = oldEuid }()
	geteuid = func() int { return 0 }

	oldUnitDir := systemdUnitDir
	defer func() { systemdUnitDir = oldUnitDir }()
	systemdUnitDir = t.TempDir()

	binDir := t.TempDir()
	writeFakeExecutable(t, binDir, "systemctl", 0, "", "")
	isolatePath(t, binDir)

	var stdout, stderr bytes.Buffer
	exitCode := runPiInstall([]string{"--user", currentTestUser(t), "--tty", "/dev/tty1"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("runPiInstall() as root = %d, stderr = %q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), tty1UnitName) {
		t.Fatalf("stdout = %q, want it to confirm the unit name", stdout.String())
	}
	unitPath := filepath.Join(systemdUnitDir, tty1UnitName)
	if _, err := os.Stat(unitPath); err != nil {
		t.Fatalf("expected unit file at %s: %v", unitPath, err)
	}
}
