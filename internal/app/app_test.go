package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/service"
	"github.com/dvgamerr/llm-status/internal/state"
)

func TestRunIngestEndToEnd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	input := bytes.NewBufferString(`{
      "session_id":"cli-test",
      "model":{"display_name":"Opus"},
      "context_window":{"used_percentage":12},
      "rate_limits":{"five_hour":{"used_percentage":34}}
    }`)
	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"ingest", "--state-dir", dir}, input, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("Run() exit = %d, stderr = %s", exitCode, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "[Opus] 5h 34% · ctx 12%" {
		t.Fatalf("stdout = %q", got)
	}
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.LoadLatest()
	if err != nil {
		t.Fatalf("LoadLatest() error = %v", err)
	}
	if snapshot.Session.ID != "cli-test" {
		t.Fatalf("stored session = %q", snapshot.Session.ID)
	}
}

func TestRunActivityEndToEndAndNeverBlocksHook(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer
	input := strings.NewReader(`{"session_id":"activity-cli","hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	if exitCode := Run(context.Background(), []string{"activity", "--state-dir", dir}, input, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("activity exit = %d", exitCode)
	}
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.LoadLatest()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity.State != model.ActivityBuilding {
		t.Fatalf("activity = %+v", snapshot.Activity)
	}

	stderr.Reset()
	if exitCode := Run(context.Background(), []string{"activity", "--state-dir", dir}, strings.NewReader("not-json"), &stdout, &stderr); exitCode != 0 || !strings.Contains(stderr.String(), "record activity") {
		t.Fatalf("invalid activity exit = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunUsageEndToEnd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	seed := bytes.NewBufferString(`{"session_id":"cli-test","model":{"display_name":"Sonnet 5"}}`)
	var seedOut, seedErr bytes.Buffer
	if exitCode := Run(context.Background(), []string{"ingest", "--state-dir", dir}, seed, &seedOut, &seedErr); exitCode != 0 {
		t.Fatalf("seed ingest exit = %d, stderr = %s", exitCode, seedErr.String())
	}

	var stdout, stderr bytes.Buffer
	args := []string{"usage", "--state-dir", dir, "--five-hour", "63", "--seven-day", "42"}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("Run() exit = %d, stderr = %s", exitCode, stderr.String())
	}

	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.LoadLatest()
	if err != nil {
		t.Fatalf("LoadLatest() error = %v", err)
	}
	if snapshot.Model.DisplayName != "Sonnet 5" {
		t.Fatalf("usage clobbered model: %+v", snapshot.Model)
	}
	if got := *snapshot.RateLimits.FiveHour.UsedPercentage; got != 63 {
		t.Fatalf("FiveHour.UsedPercentage = %v", got)
	}
	if got := *snapshot.RateLimits.SevenDay.UsedPercentage; got != 42 {
		t.Fatalf("SevenDay.UsedPercentage = %v", got)
	}
}

func TestRunRejectsInvalidInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"ingest", "--state-dir", t.TempDir()}, strings.NewReader("not-json"), &stdout, &stderr)
	if exitCode != 1 || !strings.Contains(stderr.String(), "decode statusLine JSON") {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestRunImportEndToEnd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	want := model.Snapshot{
		SchemaVersion: model.CurrentSchemaVersion,
		CapturedAt:    time.Unix(123, 0).UTC(),
		Provider:      "codex",
		Session:       model.Session{ID: "import-test"},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"import", "--state-dir", dir}, bytes.NewReader(data), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("Run(import) exit = %d, stderr = %q", exitCode, stderr.String())
	}
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadLatest()
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "codex" || got.Session.ID != "import-test" {
		t.Fatalf("imported snapshot = %+v", got)
	}
}

func TestRunCodexNotifyEndToEnd(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	stateDir := filepath.Join(root, "state")
	threadID := "thread-app-test"
	rollout := filepath.Join(home, "sessions", "rollout-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"turn_context","payload":{"model":"gpt-5.6-sol","effort":"high"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120},"model_context_window":1000},"rate_limits":{}}}`
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := `{"type":"agent-turn-complete","thread-id":"` + threadID + `","input-messages":["private"]}`
	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"codex-notify", "--state-dir", stateDir, "--codex-home", home, payload}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("Run(codex-notify) exit = %d, stderr = %q", exitCode, stderr.String())
	}
	store, err := state.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.LoadLatest()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Provider != "codex" || snapshot.Model.ID != "gpt-5.6-sol" || snapshot.Context.UsedPercentage == nil || *snapshot.Context.UsedPercentage != 12 {
		t.Fatalf("Codex snapshot = %+v", snapshot)
	}
}

func TestRunPreviewWritesPNG(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "preview.png")
	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"preview", "--state-dir", filepath.Join(root, "state"), "--output", output}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("Run(preview) exit = %d, stderr = %q", exitCode, stderr.String())
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	config, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 800 || config.Height != 480 {
		t.Fatalf("preview size = %dx%d", config.Width, config.Height)
	}
}

func TestRunHelpAndUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := Run(context.Background(), nil, strings.NewReader(""), &stdout, &stderr); exitCode != 0 || !strings.Contains(stdout.String(), "llm-status ingest") {
		t.Fatalf("help: exit=%d stdout=%q", exitCode, stdout.String())
	}
	stdout.Reset()
	if exitCode := Run(context.Background(), []string{"wat"}, strings.NewReader(""), &stdout, &stderr); exitCode != 2 || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("unknown command: exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), "llm-status dev") || stderr.Len() != 0 {
		t.Fatalf("version: exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunValidatesCommandFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantExit int
		wantText string
	}{
		{name: "ingest help", args: []string{"ingest", "--help"}, wantExit: 0, wantText: "Usage: llm-status ingest"},
		{name: "ingest positional", args: []string{"ingest", "unexpected"}, wantExit: 2, wantText: "unexpected positional"},
		{name: "ingest empty state", args: []string{"ingest", "--state-dir", ""}, wantExit: 1, wantText: "state directory is empty"},
		{name: "import help", args: []string{"import", "--help"}, wantExit: 0, wantText: "Usage: llm-status import"},
		{name: "import positional", args: []string{"import", "unexpected"}, wantExit: 2, wantText: "unexpected positional"},
		{name: "codex notify help", args: []string{"codex-notify", "--help"}, wantExit: 0, wantText: "Usage: llm-status codex-notify"},
		{name: "codex notify missing payload", args: []string{"codex-notify"}, wantExit: 2, wantText: "expected one"},
		{name: "tui help", args: []string{"tui", "--help"}, wantExit: 0, wantText: "Usage: llm-status tui"},
		{name: "tui positional", args: []string{"tui", "unexpected"}, wantExit: 2, wantText: "unexpected positional"},
		{name: "tui refresh too fast", args: []string{"tui", "--refresh", "100ms"}, wantExit: 2, wantText: "at least 250ms"},
		{name: "tui stale invalid", args: []string{"tui", "--stale-after", "0s"}, wantExit: 2, wantText: "greater than zero"},
		{name: "tui duration invalid", args: []string{"tui", "--refresh", "invalid"}, wantExit: 2, wantText: "invalid value"},
		{name: "gfx help", args: []string{"gfx", "--help"}, wantExit: 0, wantText: "Usage: llm-status gfx"},
		{name: "gfx positional", args: []string{"gfx", "unexpected"}, wantExit: 2, wantText: "unexpected positional"},
		{name: "gfx refresh too fast", args: []string{"gfx", "--refresh", "5ms"}, wantExit: 2, wantText: "at least 20ms"},
		{name: "preview help", args: []string{"preview", "--help"}, wantExit: 0, wantText: "Usage: llm-status preview"},
		{name: "preview positional", args: []string{"preview", "unexpected"}, wantExit: 2, wantText: "unexpected positional"},
		{name: "usage help", args: []string{"usage", "--help"}, wantExit: 0, wantText: "Usage: llm-status usage"},
		{name: "usage positional", args: []string{"usage", "--five-hour", "1", "--seven-day", "1", "unexpected"}, wantExit: 2, wantText: "unexpected positional"},
		{name: "usage missing flags", args: []string{"usage"}, wantExit: 2, wantText: "--five-hour and --seven-day are required"},
		{name: "usage invalid reset", args: []string{"usage", "--five-hour", "1", "--seven-day", "1", "--five-hour-reset", "0s"}, wantExit: 2, wantText: "must be positive"},
		{name: "usage missing snapshot", args: []string{"usage", "--state-dir", t.TempDir(), "--five-hour", "1", "--seven-day", "1"}, wantExit: 1, wantText: "load existing snapshot"},
		{name: "relay help", args: []string{"relay", "--help"}, wantExit: 0, wantText: "Usage: llm-status relay"},
		{name: "relay missing host", args: []string{"relay", "--once"}, wantExit: 2, wantText: "--mirror-ssh is required"},
		{name: "relay positional", args: []string{"relay", "--mirror-ssh", "pilab", "unexpected"}, wantExit: 2, wantText: "unexpected positional"},
		{name: "relay refresh too fast", args: []string{"relay", "--mirror-ssh", "pilab", "--refresh", "10ms"}, wantExit: 2, wantText: "at least 100ms"},
		{name: "service install help", args: []string{"service", "install", "--help"}, wantExit: 0, wantText: "Usage: llm-status service install"},
		{name: "service install invalid flag", args: []string{"service", "install", "--invalid"}, wantExit: 2, wantText: "flag provided but not defined"},
		{name: "service install missing host", args: []string{"service", "install"}, wantExit: 2, wantText: "--mirror-ssh is required"},
		{name: "service install refresh too fast", args: []string{"service", "install", "--mirror-ssh", "pilab", "--refresh", "10ms"}, wantExit: 2, wantText: "at least 100ms"},
		{name: "service install positional", args: []string{"service", "install", "--mirror-ssh", "pilab", "unexpected"}, wantExit: 2, wantText: "unexpected positional"},
		{name: "pi install help", args: []string{"pi", "install", "--help"}, wantExit: 0, wantText: "Usage: llm-status pi install"},
		{name: "pi install refresh too fast", args: []string{"pi", "install", "--refresh", "1ms"}, wantExit: 2, wantText: "at least 20ms"},
		{name: "pi install positional", args: []string{"pi", "install", "unexpected"}, wantExit: 2, wantText: "unexpected positional"},
		{name: "ingest legacy mirror removed", args: []string{"ingest", "--mirror-ssh", "pilab"}, wantExit: 2, wantText: "flag provided but not defined"},
		{name: "activity legacy mirror removed", args: []string{"activity", "--mirror-ssh", "pilab"}, wantExit: 0, wantText: "flag provided but not defined"},
		{name: "usage legacy mirror removed", args: []string{"usage", "--mirror-ssh", "pilab"}, wantExit: 2, wantText: "flag provided but not defined"},
		{name: "codex legacy mirror removed", args: []string{"codex-notify", "--mirror-ssh", "pilab"}, wantExit: 2, wantText: "flag provided but not defined"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode := Run(context.Background(), tt.args, strings.NewReader(""), &stdout, &stderr)
			combined := stdout.String() + stderr.String()
			if exitCode != tt.wantExit || !strings.Contains(combined, tt.wantText) {
				t.Fatalf("exit=%d output=%q, want exit=%d containing %q", exitCode, combined, tt.wantExit, tt.wantText)
			}
		})
	}
}

func TestRunRelayOnceWithEmptyAndRejectedSnapshots(t *testing.T) {
	emptyDir := filepath.Join(t.TempDir(), "empty-state")
	var stdout, stderr bytes.Buffer
	args := []string{"relay", "--once", "--mirror-ssh", "pilab", "--state-dir", emptyDir}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("empty relay exit = %d, stderr = %q", exitCode, stderr.String())
	}

	store, err := state.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(model.Snapshot{SchemaVersion: model.CurrentSchemaVersion, CapturedAt: time.Now().UTC(), Provider: model.ProviderClaude, Session: model.Session{ID: "relay-test"}}); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	args = []string{"relay", "--once", "--mirror-ssh", "bad;host", "--state-dir", store.Dir()}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "invalid SSH mirror host") {
		t.Fatalf("rejected relay exit = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunDispatchesPiServiceGFXAndPreviewErrors(t *testing.T) {
	tests := []struct {
		args     []string
		wantExit int
		wantText string
	}{
		{[]string{"pi"}, 2, "Usage: llm-status pi"},
		{[]string{"pi", "help"}, 0, "Usage: llm-status pi install"},
		{[]string{"pi", "unknown"}, 2, "unknown subcommand"},
		{[]string{"service"}, 2, "Usage: llm-status service"},
		{[]string{"service", "help"}, 0, "Usage: llm-status service"},
		{[]string{"service", "unknown"}, 2, "unknown subcommand"},
		{[]string{"gfx", "--touch-device", ""}, 1, "open framebuffer"},
		{[]string{"preview", "--output", filepath.Join(t.TempDir(), "missing", "out.png")}, 1, "create output"},
		{[]string{"codex-notify", `{"type":"other","thread-id":"thread"}`}, 1, "notification type"},
		{[]string{"import"}, 1, "sanitized snapshot is empty"},
	}
	if runtime.GOOS != "linux" {
		tests = append(tests, struct {
			args     []string
			wantExit int
			wantText string
		}{[]string{"pi", "install"}, 1, "only supported on Linux"})
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		exitCode := Run(context.Background(), test.args, strings.NewReader(""), &stdout, &stderr)
		combined := stdout.String() + stderr.String()
		if exitCode != test.wantExit || !strings.Contains(combined, test.wantText) {
			t.Fatalf("Run(%v) = %d, %q; want %d containing %q", test.args, exitCode, combined, test.wantExit, test.wantText)
		}
	}
}

func TestRunServiceControlUsesInjectedOperations(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := func(string) (service.State, error) { return service.StateRunning, nil }
	if exitCode := runServiceControl(func(string) error { return nil }, status, "start", &stdout, &stderr); exitCode != 0 || !strings.Contains(stdout.String(), "running") {
		t.Fatalf("successful control = %d, %q", exitCode, stdout.String())
	}
	stdout.Reset()
	if exitCode := runServiceControl(func(string) error { return errors.New("boom") }, status, "start", &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "boom") {
		t.Fatalf("failed control = %d, %q", exitCode, stderr.String())
	}
	stdout.Reset()
	if exitCode := runServiceControl(func(string) error { return nil }, func(string) (service.State, error) { return service.StateStopped, errors.New("status") }, "stop", &stdout, &stderr); exitCode != 0 || !strings.Contains(stdout.String(), "stop requested") {
		t.Fatalf("status fallback = %d, %q", exitCode, stdout.String())
	}
}

func TestStringListAndForwardNotification(t *testing.T) {
	var values stringList
	if err := values.Set("one"); err != nil {
		t.Fatal(err)
	}
	_ = values.Set("two")
	if got := values.String(); got != "one,two" {
		t.Fatalf("String() = %q", got)
	}

	t.Setenv("GO_WANT_FORWARD_HELPER", "1")
	program := os.Args[0]
	args := []string{"-test.run=TestForwardNotificationHelper", "--"}
	if err := forwardNotification(context.Background(), program, args, "ok"); err != nil {
		t.Fatalf("forward success error = %v", err)
	}
	if err := forwardNotification(context.Background(), program, args, "fail"); err == nil || !strings.Contains(err.Error(), "helper failure") {
		t.Fatalf("forward failure error = %v", err)
	}
}

func TestForwardNotificationHelper(_ *testing.T) {
	if os.Getenv("GO_WANT_FORWARD_HELPER") != "1" {
		return
	}
	if os.Args[len(os.Args)-1] == "fail" {
		fmt.Fprintln(os.Stderr, "helper failure")
		os.Exit(7)
	}
	os.Exit(0)
}

func TestPiUnitAndOptionHelpers(t *testing.T) {
	var stderr bytes.Buffer
	options, exitCode, parsed := parsePiInstallOptions([]string{"--user", "demo", "--refresh", "100ms", "--touch-device", ""}, &stderr)
	if !parsed || exitCode != 0 || options.user != "demo" || options.refresh != 100*time.Millisecond || options.touchDevice != "" {
		t.Fatalf("parsePiInstallOptions() = %+v, %d, %v", options, exitCode, parsed)
	}
	unit, err := buildPiUnit(piUnitConfig{
		executable:  "/opt/status app/claude%$status",
		refresh:     66 * time.Millisecond,
		framebuffer: "/dev/fb 0",
		tty:         "/dev/tty1",
		touchDevice: "/dev/input/event0",
		user:        "pi",
		group:       "pi",
		home:        "/home/pi",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Type=exec`, `"/opt/status app/claude%%$$status"`, `"/dev/fb 0"`, `--touch-device`, `User="pi"`} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit does not contain %q:\n%s", want, unit)
		}
	}
	if _, err := buildPiUnit(piUnitConfig{executable: "/bin/status", refresh: time.Second, tty: "/dev/tty1", user: "bad\nuser"}); err == nil {
		t.Fatal("buildPiUnit accepted a control character")
	}
}

func TestOpenRelayLog(t *testing.T) {
	var stderr bytes.Buffer
	output, file, err := openRelayLog("", &stderr)
	if err != nil || file != nil || output != &stderr {
		t.Fatalf("empty openRelayLog() = %T, %v, %v", output, file, err)
	}
	path := filepath.Join(t.TempDir(), "nested", "relay.log")
	output, file, err = openRelayLog(path, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if _, writeErr := io.WriteString(output, "logged"); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "logged" || stderr.String() != "logged" {
		t.Fatalf("relay log data=%q stderr=%q error=%v", data, stderr.String(), err)
	}
}

func TestRunReportsOutputFailures(t *testing.T) {
	writer := alwaysErrorWriter{}
	if exitCode := Run(context.Background(), nil, strings.NewReader(""), writer, io.Discard); exitCode != 1 {
		t.Fatalf("help output failure exit = %d", exitCode)
	}
	if exitCode := Run(context.Background(), []string{"version"}, strings.NewReader(""), writer, io.Discard); exitCode != 1 {
		t.Fatalf("version output failure exit = %d", exitCode)
	}
	if exitCode := Run(context.Background(), []string{"unknown"}, strings.NewReader(""), io.Discard, writer); exitCode != 1 {
		t.Fatalf("error output failure exit = %d", exitCode)
	}
}

type alwaysErrorWriter struct{}

func (alwaysErrorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// failAfterNWriter succeeds on the first n writes, then fails every write
// after that. It exercises the second Write call in a code path (e.g. the
// unknown-command message followed by printUsage) without breaking the
// first.
type failAfterNWriter struct{ remaining int }

func (w *failAfterNWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, errors.New("write failed")
	}
	w.remaining--
	return len(p), nil
}

// clearStateDirEnv unsets every environment variable state.DefaultDir and
// os.UserCacheDir consult, on every platform, so a test can force
// state.DefaultDir to return an error without needing a --state-dir flag.
func clearStateDirEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"LLM_STATUS_STATE_DIR", "LocalAppData", "HOME", "XDG_CACHE_HOME", "home"} {
		t.Setenv(name, "")
	}
}

func TestRunTopLevelHelpVariantsAndOutputFailure(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 || !strings.Contains(stdout.String(), "llm-status ingest") {
			t.Fatalf("Run(%v) exit=%d stdout=%q", args, exitCode, stdout.String())
		}
	}

	// printUsage itself failing to write (top-level help).
	if exitCode := Run(context.Background(), []string{"help"}, strings.NewReader(""), alwaysErrorWriter{}, io.Discard); exitCode != 1 {
		t.Fatalf("help output failure exit = %d", exitCode)
	}

	// The unknown-command branch writes twice: the "unknown command"
	// message, then printUsage. Let the first succeed and the second fail
	// to reach that inner error branch specifically.
	writer := &failAfterNWriter{remaining: 1}
	if exitCode := Run(context.Background(), []string{"wat"}, strings.NewReader(""), io.Discard, writer); exitCode != 1 {
		t.Fatalf("unknown command printUsage failure exit = %d", exitCode)
	}
}

func TestRunDefaultStateDirResolutionErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantExit int
	}{
		{"ingest", []string{"ingest"}, 1},
		{"activity", []string{"activity"}, 0},
		{"usage", []string{"usage", "--five-hour", "1", "--seven-day", "1"}, 1},
		{"import", []string{"import"}, 1},
		{"codex-notify", []string{"codex-notify", `{"type":"agent-turn-complete","thread-id":"abc12345"}`}, 1},
		{"relay", []string{"relay", "--once", "--mirror-ssh", "pilab"}, 1},
		{"tui", []string{"tui"}, 1},
		{"gfx", []string{"gfx"}, 1},
		{"preview", []string{"preview"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearStateDirEnv(t)
			var stdout, stderr bytes.Buffer
			exitCode := Run(context.Background(), tt.args, strings.NewReader(""), &stdout, &stderr)
			if exitCode != tt.wantExit || !strings.Contains(stderr.String(), "resolve state directory") {
				t.Fatalf("Run(%v) = %d, %q; want %d containing \"resolve state directory\"", tt.args, exitCode, stderr.String(), tt.wantExit)
			}
		})
	}
}

func TestRunCodexNotifyDefaultHomeError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("USERPROFILE/HOMEDRIVE/HOMEPATH isolation is Windows-specific; other platforms share $HOME with state.DefaultDir")
	}
	t.Setenv("CODEX_HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	var stdout, stderr bytes.Buffer
	payload := `{"type":"agent-turn-complete","thread-id":"abc12345"}`
	exitCode := Run(context.Background(), []string{"codex-notify", "--state-dir", t.TempDir(), payload}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 1 || !strings.Contains(stderr.String(), "resolve codex home") {
		t.Fatalf("Run(codex-notify) = %d, %q", exitCode, stderr.String())
	}
}

func TestRunActivityPositionalArgsAndStoreError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := Run(context.Background(), []string{"activity", "--state-dir", t.TempDir(), "unexpected"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 || !strings.Contains(stderr.String(), "unexpected positional") {
		t.Fatalf("activity positional args = %d, %q", exitCode, stderr.String())
	}
	stderr.Reset()
	if exitCode := Run(context.Background(), []string{"activity", "--state-dir", ""}, strings.NewReader(`{"hook_event_name":"Stop"}`), &stdout, &stderr); exitCode != 0 || !strings.Contains(stderr.String(), "open state store") {
		t.Fatalf("activity store error = %d, %q", exitCode, stderr.String())
	}
}

func TestRunUsageStoreError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"usage", "--five-hour", "1", "--seven-day", "1", "--state-dir", ""}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "open state store") {
		t.Fatalf("usage store error = %d, %q", exitCode, stderr.String())
	}
}

func TestRunImportRejectsUnknownFieldsMalformedJSONAndSaveErrors(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	unknown := `{"schema_version":1,"captured_at":"2024-01-01T00:00:00Z","provider":"codex","session":{"id":"x"},"totally_unknown_field":true}`
	if exitCode := Run(context.Background(), []string{"import", "--state-dir", dir}, strings.NewReader(unknown), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "decode sanitized snapshot") {
		t.Fatalf("import unknown field = %d, %q", exitCode, stderr.String())
	}

	stderr.Reset()
	if exitCode := Run(context.Background(), []string{"import", "--state-dir", dir}, strings.NewReader("{not-json"), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "decode sanitized snapshot") {
		t.Fatalf("import malformed JSON = %d, %q", exitCode, stderr.String())
	}

	stderr.Reset()
	if exitCode := Run(context.Background(), []string{"import", "--state-dir", ""}, strings.NewReader(unknown), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "open state store") {
		t.Fatalf("import store error = %d, %q", exitCode, stderr.String())
	}

	// Valid, field-clean JSON that decodes but fails model.Snapshot's own
	// Save-time validation (schema version 0), to reach the Save error
	// branch distinctly from the decode error branch above.
	stderr.Reset()
	if exitCode := Run(context.Background(), []string{"import", "--state-dir", dir}, strings.NewReader("{}"), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "save snapshot") {
		t.Fatalf("import save schema error = %d, %q", exitCode, stderr.String())
	}

	// Save failing for an unrelated reason (state dir collides with a file)
	// while decode succeeds with a fully valid snapshot.
	root := t.TempDir()
	blocker := filepath.Join(root, "state")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := `{"schema_version":1,"captured_at":"2024-01-01T00:00:00Z","provider":"codex","session":{"id":"import-save-error"}}`
	stderr.Reset()
	if exitCode := Run(context.Background(), []string{"import", "--state-dir", blocker}, strings.NewReader(valid), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "save snapshot") {
		t.Fatalf("import save collision error = %d, %q", exitCode, stderr.String())
	}
}

func TestRunCodexNotifyForwardWarningSnapshotAndStoreErrors(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	threadID := "thread-app-test-2"
	rollout := filepath.Join(home, "sessions", "rollout-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"turn_context","payload":{"model":"gpt-5.6-sol","effort":"high"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"model_context_window":10},"rate_limits":{}}}`
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := `{"type":"agent-turn-complete","thread-id":"` + threadID + `"}`

	// The pre-existing notifier fails to forward (nonexistent program), but
	// that must only be a warning: the rest of the pipeline still succeeds.
	stateDir := filepath.Join(root, "state")
	var stdout, stderr bytes.Buffer
	args := []string{"codex-notify", "--state-dir", stateDir, "--codex-home", home, "--forward", filepath.Join(root, "no-such-notifier"), payload}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 || !strings.Contains(stderr.String(), "forward codex notification") {
		t.Fatalf("codex-notify forward warning = %d, %q", exitCode, stderr.String())
	}

	// A well-formed notification whose rollout cannot be found still exits
	// cleanly (not a crash), reporting the build-snapshot error.
	stderr.Reset()
	missingHome := filepath.Join(root, "codex-empty")
	args = []string{"codex-notify", "--state-dir", stateDir, "--codex-home", missingHome, `{"type":"agent-turn-complete","thread-id":"no-rollout-thread"}`}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "build snapshot from notification") {
		t.Fatalf("codex-notify missing rollout = %d, %q", exitCode, stderr.String())
	}

	// store.New itself failing.
	stderr.Reset()
	args = []string{"codex-notify", "--state-dir", "", "--codex-home", home, payload}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "open state store") {
		t.Fatalf("codex-notify store error = %d, %q", exitCode, stderr.String())
	}

	// Save failing after a fully valid snapshot was built.
	blocker := filepath.Join(root, "blocked-state")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	args = []string{"codex-notify", "--state-dir", blocker, "--codex-home", home, payload}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "save snapshot") {
		t.Fatalf("codex-notify save error = %d, %q", exitCode, stderr.String())
	}
}

func TestRunRelayContextCancellationStoreAndLogErrors(t *testing.T) {
	// store.New failing.
	var stdout, stderr bytes.Buffer
	args := []string{"relay", "--mirror-ssh", "pilab", "--state-dir", ""}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "open state store") {
		t.Fatalf("relay store error = %d, %q", exitCode, stderr.String())
	}

	// openRelayLog failing.
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	args = []string{"relay", "--once", "--mirror-ssh", "pilab", "--state-dir", filepath.Join(root, "state"), "--log-file", filepath.Join(blocker, "sub", "relay.log")}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "open log file") {
		t.Fatalf("relay log file error = %d, %q", exitCode, stderr.String())
	}

	// A working --log-file, exercised via a normal --once run, so the defer
	// that closes it (and the success side of that close) actually runs.
	stderr.Reset()
	logPath := filepath.Join(root, "relay.log")
	args = []string{"relay", "--once", "--mirror-ssh", "pilab", "--state-dir", filepath.Join(root, "state2"), "--log-file", logPath}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("relay with log file = %d, %q", exitCode, stderr.String())
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected log file to exist: %v", err)
	}

	// An already-cancelled context makes the non-once loop return quickly
	// via the ctx.Done() branch instead of hanging.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"relay", "--mirror-ssh", "pilab", "--state-dir", filepath.Join(root, "state3")}, strings.NewReader(""), &stdout, &stderr)
	}()
	select {
	case exitCode := <-done:
		if exitCode != 0 {
			t.Fatalf("cancelled relay exit = %d", exitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not return promptly for an already-cancelled context")
	}

	// A short-lived context lets at least one refresh-ticker tick fire
	// before ctx.Done(), covering that select branch too, still bounded
	// well under the no-hang budget.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 260*time.Millisecond)
	defer cancel2()
	done2 := make(chan int, 1)
	go func() {
		done2 <- Run(ctx2, []string{"relay", "--mirror-ssh", "pilab", "--state-dir", filepath.Join(root, "state4"), "--refresh", "100ms"}, strings.NewReader(""), &stdout, &stderr)
	}()
	select {
	case exitCode := <-done2:
		if exitCode != 0 {
			t.Fatalf("ticking relay exit = %d", exitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not return promptly after its context timeout")
	}
}

func TestOpenRelayLogMkdirAndOpenFileErrors(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if _, _, err := openRelayLog(filepath.Join(blocker, "sub", "relay.log"), &stderr); err == nil {
		t.Fatal("want mkdir error when a path component is a file")
	}

	dirPath := filepath.Join(root, "isdir")
	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRelayLog(dirPath, &stderr); err == nil {
		t.Fatal("want open error when the log path is itself a directory")
	}
}

func TestForwardNotificationEmptyStderrDetail(t *testing.T) {
	err := forwardNotification(context.Background(), filepath.Join(t.TempDir(), "does-not-exist-binary"), nil, "payload")
	if err == nil {
		t.Fatal("want error for a nonexistent notifier")
	}
	if strings.Count(err.Error(), ":") == 0 {
		t.Fatalf("unexpected error shape: %v", err)
	}
}

func TestRunTUIQuitsCleanlyAndReportsErrors(t *testing.T) {
	dir := t.TempDir()

	// store.New failing.
	var stdout, stderr bytes.Buffer
	if exitCode := Run(context.Background(), []string{"tui", "--state-dir", ""}, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "open state store") {
		t.Fatalf("tui store error = %d, %q", exitCode, stderr.String())
	}

	// A "q" keypress reaches dashboard.Run's normal tea.Quit path, so Run
	// returns 0 without needing a real terminal or the alternate screen.
	stdout.Reset()
	stderr.Reset()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"tui", "--state-dir", dir, "--inline"}, strings.NewReader("q"), &stdout, &stderr)
	}()
	select {
	case exitCode := <-done:
		if exitCode != 0 {
			t.Fatalf("tui quit exit = %d, stderr = %q", exitCode, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tui did not quit promptly on 'q'")
	}

	// An already-cancelled context makes dashboard.Run report a "program
	// was killed" error instead of ever starting the interactive loop.
	stdout.Reset()
	stderr.Reset()
	cancelledCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	done2 := make(chan int, 1)
	go func() {
		done2 <- Run(cancelledCtx, []string{"tui", "--state-dir", dir, "--inline"}, strings.NewReader(""), &stdout, &stderr)
	}()
	select {
	case exitCode := <-done2:
		if exitCode != 1 || !strings.Contains(stderr.String(), "tui") {
			t.Fatalf("tui cancelled-context exit = %d, stderr = %q", exitCode, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tui did not return promptly for an already-cancelled context")
	}
}

func TestRunGFXStaleAfterAndStoreErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := Run(context.Background(), []string{"gfx", "--stale-after", "0s"}, strings.NewReader(""), &stdout, &stderr); exitCode != 2 || !strings.Contains(stderr.String(), "greater than zero") {
		t.Fatalf("gfx stale-after invalid = %d, %q", exitCode, stderr.String())
	}
	stderr.Reset()
	if exitCode := Run(context.Background(), []string{"gfx", "--state-dir", ""}, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "open state store") {
		t.Fatalf("gfx store error = %d, %q", exitCode, stderr.String())
	}
}

func TestRunPreviewAtFlagAndStoreErrors(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer

	if exitCode := Run(context.Background(), []string{"preview", "--state-dir", filepath.Join(root, "state"), "--at", "not-a-timestamp"}, strings.NewReader(""), &stdout, &stderr); exitCode != 2 || !strings.Contains(stderr.String(), "parse --at") {
		t.Fatalf("preview malformed --at = %d, %q", exitCode, stderr.String())
	}

	stderr.Reset()
	output := filepath.Join(root, "at-preview.png")
	args := []string{"preview", "--state-dir", filepath.Join(root, "state"), "--output", output, "--at", "2026-01-02T03:04:05Z"}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("preview valid --at = %d, %q", exitCode, stderr.String())
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("expected preview PNG: %v", err)
	}

	stderr.Reset()
	if exitCode := Run(context.Background(), []string{"preview", "--state-dir", ""}, strings.NewReader(""), &stdout, &stderr); exitCode != 1 || !strings.Contains(stderr.String(), "open state store") {
		t.Fatalf("preview store error = %d, %q", exitCode, stderr.String())
	}
}

func TestRunPreviewRendersWithCorruptedSnapshot(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	sessionsDir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "corrupt.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "corrupted-preview.png")
	var stdout, stderr bytes.Buffer
	args := []string{"preview", "--state-dir", stateDir, "--output", output}
	if exitCode := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("preview with corrupted snapshot = %d, %q", exitCode, stderr.String())
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := png.DecodeConfig(file); err != nil {
		t.Fatalf("expected a valid PNG despite the corrupted snapshot: %v", err)
	}
}
