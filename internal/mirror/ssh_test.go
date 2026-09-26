package mirror

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
)

func TestSSHRejectsUnsafeTargetsBeforeLaunching(t *testing.T) {
	tests := []struct {
		name string
		host string
		bin  string
	}{
		{name: "empty host", bin: DefaultRemoteBinary},
		{name: "option host", host: "-oProxyCommand=bad", bin: DefaultRemoteBinary},
		{name: "shell host", host: "pi;bad", bin: DefaultRemoteBinary},
		{name: "shell binary", host: "pilab", bin: "/tmp/a;bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SSH(context.Background(), tt.host, tt.bin, model.Snapshot{})
			if err == nil || (!strings.Contains(err.Error(), "host") && !strings.Contains(err.Error(), "binary")) {
				t.Fatalf("SSH() error = %v", err)
			}
		})
	}
}

func TestSSHInvokesExpectedCommandAndReportsBoundedFailure(t *testing.T) {
	original := commandContext
	defer func() { commandContext = original }()
	var captured []string
	mode := "success"
	commandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestSSHHelperProcess", "--", mode)
	}
	snapshot := model.Snapshot{SchemaVersion: model.CurrentSchemaVersion, Session: model.Session{ID: "test"}}
	if err := SSH(context.Background(), "pi@example:22", "/opt/status", snapshot); err != nil {
		t.Fatalf("SSH() success error = %v", err)
	}
	want := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "pi@example:22", "/opt/status", "import"}
	if strings.Join(captured, "|") != strings.Join(want, "|") {
		t.Fatalf("command args = %q, want %q", captured, want)
	}

	mode = "fail"
	if err := SSH(context.Background(), "pilab", DefaultRemoteBinary, snapshot); err == nil || !strings.Contains(err.Error(), "helper failure") {
		t.Fatalf("SSH() failure error = %v", err)
	}
}

func TestSSHHelperProcess(_ *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "fail":
		fmt.Fprintln(os.Stderr, "helper failure")
		os.Exit(9)
	case "fail-silent":
		os.Exit(9)
	}
	os.Exit(0)
}

// TestSSHReportsEncodeErrorForUnmarshalableSnapshot pins the one way
// json.Marshal can fail for a Snapshot: time.Time.MarshalJSON rejects years
// outside [0,9999]. This is constructed directly rather than round-tripped
// through disk, matching the same encode-error technique used for
// relay.snapshotFingerprint.
func TestSSHReportsEncodeErrorForUnmarshalableSnapshot(t *testing.T) {
	snapshot := model.Snapshot{
		Session:    model.Session{ID: "bad-time"},
		CapturedAt: time.Date(99999, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	err := SSH(context.Background(), "pilab", DefaultRemoteBinary, snapshot)
	if err == nil || !strings.Contains(err.Error(), "encode sanitized snapshot") {
		t.Fatalf("SSH() error = %v, want an encode error", err)
	}
}

// TestSSHReportsTimeoutWhenContextExpiresBeforeCommandCompletes pins the
// branch that prefers the deadline explanation over the raw exec error: a
// caller context that is already expired (or expires immediately) by the
// time command.Run() fails must surface "context deadline exceeded" via
// timeoutCtx.Err() rather than the underlying process error.
func TestSSHReportsTimeoutWhenContextExpiresBeforeCommandCompletes(t *testing.T) {
	original := commandContext
	defer func() { commandContext = original }()
	commandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestSSHHelperProcess", "--", "fail")
	}

	expired, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-expired.Done()

	snapshot := model.Snapshot{SchemaVersion: model.CurrentSchemaVersion, Session: model.Session{ID: "test"}}
	err := SSH(expired, "pilab", DefaultRemoteBinary, snapshot)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("SSH() error = %v, want a context deadline exceeded error", err)
	}
}

// TestSSHOmitsDetailSuffixWhenStderrIsEmpty pins the fallback error format
// used when the failing command wrote nothing to stderr: the returned error
// must not gain a stray ": " suffix from an empty detail string.
func TestSSHOmitsDetailSuffixWhenStderrIsEmpty(t *testing.T) {
	original := commandContext
	defer func() { commandContext = original }()
	commandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestSSHHelperProcess", "--", "fail-silent")
	}

	snapshot := model.Snapshot{SchemaVersion: model.CurrentSchemaVersion, Session: model.Session{ID: "test"}}
	err := SSH(context.Background(), "pilab", DefaultRemoteBinary, snapshot)
	if err == nil || strings.Contains(err.Error(), ": : ") || strings.HasSuffix(err.Error(), ": ") {
		t.Fatalf("SSH() error = %v, want no empty detail suffix", err)
	}
	if !strings.Contains(err.Error(), "mirror sanitized snapshot to pilab") {
		t.Fatalf("SSH() error = %v, missing expected prefix", err)
	}
}
