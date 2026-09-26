package app

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dvgamerr/llm-status/internal/service"
)

func withServiceFakes(t *testing.T, install func(service.Config) error, remove, start, stop func(string) error, status func(string) (service.State, error)) {
	t.Helper()
	oldInstall, oldRemove, oldStart, oldStop, oldStatus := serviceInstall, serviceRemove, serviceStart, serviceStop, serviceStatus
	if install != nil {
		serviceInstall = install
	}
	if remove != nil {
		serviceRemove = remove
	}
	if start != nil {
		serviceStart = start
	}
	if stop != nil {
		serviceStop = stop
	}
	if status != nil {
		serviceStatus = status
	}
	t.Cleanup(func() {
		serviceInstall, serviceRemove, serviceStart, serviceStop, serviceStatus = oldInstall, oldRemove, oldStart, oldStop, oldStatus
	})
}

func TestRunServiceUsageAndUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := runService(nil, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("runService(nil) = %d, want 2", exitCode)
	}
	stderr.Reset()
	if exitCode := runService([]string{"bogus"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("runService(bogus) = %d, want 2", exitCode)
	}
	stdout.Reset()
	if exitCode := runService([]string{"help"}, &stdout, &stderr); exitCode != 0 || !strings.Contains(stdout.String(), "Usage") {
		t.Fatalf("runService(help) = %d, stdout = %q", exitCode, stdout.String())
	}
}

func TestRunServiceUsageAndHelpWriteFailures(t *testing.T) {
	if exitCode := runService(nil, &bytes.Buffer{}, failingWriter{}); exitCode != 1 {
		t.Fatalf("runService(nil) with failing stderr = %d, want 1", exitCode)
	}
	if exitCode := runService([]string{"help"}, failingWriter{}, &bytes.Buffer{}); exitCode != 1 {
		t.Fatalf("runService(help) with failing stdout = %d, want 1", exitCode)
	}
}

func TestRunServiceDispatchesToInstall(t *testing.T) {
	var stderr bytes.Buffer
	// Bad flags make runServiceInstall return quickly without needing the
	// full install seam — this just proves runService's "install" case
	// actually reaches runServiceInstall.
	if exitCode := runService([]string{"install"}, &bytes.Buffer{}, &stderr); exitCode != 2 {
		t.Fatalf("runService(install) = %d, want 2 (missing --mirror-ssh)", exitCode)
	}
}

func TestRunServiceRemoveSuccessAndFailure(t *testing.T) {
	withServiceFakes(t, nil, func(string) error { return nil }, nil, nil, nil)
	var stdout, stderr bytes.Buffer
	if exitCode := runService([]string{"remove"}, &stdout, &stderr); exitCode != 0 || !strings.Contains(stdout.String(), RelayServiceName) {
		t.Fatalf("runService(remove) = %d, stdout = %q", exitCode, stdout.String())
	}

	withServiceFakes(t, nil, func(string) error { return errors.New("boom") }, nil, nil, nil)
	stdout.Reset()
	stderr.Reset()
	if exitCode := runService([]string{"remove"}, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("runService(remove) with failing Remove = %d, want 1", exitCode)
	}
}

func TestRunServiceStatus(t *testing.T) {
	withServiceFakes(t, nil, nil, nil, nil, func(string) (service.State, error) { return service.StateRunning, nil })
	var stdout, stderr bytes.Buffer
	if exitCode := runService([]string{"status"}, &stdout, &stderr); exitCode != 0 || !strings.Contains(stdout.String(), service.StateRunning.String()) {
		t.Fatalf("runService(status) = %d, stdout = %q", exitCode, stdout.String())
	}

	withServiceFakes(t, nil, nil, nil, nil, func(string) (service.State, error) { return service.StateNotInstalled, errors.New("boom") })
	stdout.Reset()
	stderr.Reset()
	if exitCode := runService([]string{"status"}, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("runService(status) with failing Status = %d, want 1", exitCode)
	}
}

func TestRunServiceStartAndStopDispatch(t *testing.T) {
	var started, stopped bool
	withServiceFakes(t, nil, nil,
		func(string) error { started = true; return nil },
		func(string) error { stopped = true; return nil },
		func(string) (service.State, error) { return service.StateRunning, nil },
	)
	var stdout, stderr bytes.Buffer
	if exitCode := runService([]string{"start"}, &stdout, &stderr); exitCode != 0 || !started {
		t.Fatalf("runService(start) = %d, started = %v", exitCode, started)
	}
	stdout.Reset()
	if exitCode := runService([]string{"stop"}, &stdout, &stderr); exitCode != 0 || !stopped {
		t.Fatalf("runService(stop) = %d, stopped = %v", exitCode, stopped)
	}
}

func TestRunServiceControlWriteFailures(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := runServiceControl(
		func(string) error { return nil },
		func(string) (service.State, error) { return service.StateNotInstalled, errors.New("no status") },
		"start", failingWriter{}, &stderr,
	)
	if exitCode != 1 {
		t.Fatalf("runServiceControl() status-fallback write failure = %d, want 1", exitCode)
	}

	exitCode = runServiceControl(
		func(string) error { return nil },
		func(string) (service.State, error) { return service.StateRunning, nil },
		"start", failingWriter{}, &stderr,
	)
	if exitCode != 1 {
		t.Fatalf("runServiceControl() final write failure = %d, want 1", exitCode)
	}
}

func TestRunServiceInstallRejectsBadFlags(t *testing.T) {
	var stderr bytes.Buffer
	if exitCode := runServiceInstall(nil, &stderr); exitCode != 2 {
		t.Fatalf("runServiceInstall(nil) = %d, want 2 (missing --mirror-ssh)", exitCode)
	}

	stderr.Reset()
	if exitCode := runServiceInstall([]string{"--mirror-ssh", "pilab", "--refresh", "1ms"}, &stderr); exitCode != 2 {
		t.Fatalf("runServiceInstall() with tiny refresh = %d, want 2", exitCode)
	}

	stderr.Reset()
	if exitCode := runServiceInstall([]string{"--mirror-ssh", "pilab", "extra-positional-arg"}, &stderr); exitCode != 2 {
		t.Fatalf("runServiceInstall() with positional arg = %d, want 2", exitCode)
	}
}

func TestRunServiceInstallSuccess(t *testing.T) {
	var captured service.Config
	withServiceFakes(t, func(cfg service.Config) error { captured = cfg; return nil }, nil, nil, nil, nil)
	dir := t.TempDir()
	var stderr bytes.Buffer
	exitCode := runServiceInstall([]string{
		"--mirror-ssh", "pilab",
		"--state-dir", dir,
		"--log-file", filepath.Join(dir, "relay.log"),
	}, &stderr)
	if exitCode != 0 {
		t.Fatalf("runServiceInstall() = %d, stderr = %q", exitCode, stderr.String())
	}
	if captured.Name != RelayServiceName {
		t.Fatalf("installed config name = %q, want %q", captured.Name, RelayServiceName)
	}
	found := false
	for _, arg := range captured.Args {
		if arg == "pilab" {
			found = true
		}
	}
	if !found {
		t.Fatalf("installed config args = %v, want them to include --mirror-ssh's value", captured.Args)
	}
}

func TestRunServiceInstallFailure(t *testing.T) {
	withServiceFakes(t, func(service.Config) error { return errors.New("install broke") }, nil, nil, nil, nil)
	var stderr bytes.Buffer
	exitCode := runServiceInstall([]string{"--mirror-ssh", "pilab", "--state-dir", t.TempDir()}, &stderr)
	if exitCode != 1 {
		t.Fatalf("runServiceInstall() with failing Install = %d, want 1", exitCode)
	}
}
