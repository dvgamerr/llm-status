// Package app implements llm-status command dispatch and lifecycle.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dvgamerr/llm-status/internal/activity"
	"github.com/dvgamerr/llm-status/internal/codex"
	"github.com/dvgamerr/llm-status/internal/dashboard"
	"github.com/dvgamerr/llm-status/internal/framebuffer"
	"github.com/dvgamerr/llm-status/internal/ingest"
	"github.com/dvgamerr/llm-status/internal/limitio"
	"github.com/dvgamerr/llm-status/internal/logging"
	"github.com/dvgamerr/llm-status/internal/mirror"
	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/pixelui"
	"github.com/dvgamerr/llm-status/internal/relay"
	"github.com/dvgamerr/llm-status/internal/state"
	"github.com/dvgamerr/llm-status/internal/systeminfo"
	"github.com/dvgamerr/llm-status/internal/touch"
	"github.com/dvgamerr/llm-status/internal/usage"
	"github.com/dvgamerr/llm-status/internal/winconsole"
)

var (
	// Version is the release version injected at build time.
	Version = "dev"
	// Commit is the source revision injected at build time.
	Commit = "none"
	// Date is the UTC build timestamp injected at build time.
	Date = "unknown"
)

// newCommandFlagSet centralizes the output and usage setup shared by every
// command. Keeping this in one place prevents subtle differences in help and
// parse-error behavior as new subcommands are added.
func newCommandFlagSet(name, usage string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprintln(stderr, usage) }
	return flags
}

// parseCommandFlags translates flag's parse result to the CLI exit-code
// contract. parsed is false when the caller should return exitCode directly.
func parseCommandFlags(flags *flag.FlagSet, args []string) (exitCode int, parsed bool) {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	return 0, true
}

// Run executes one CLI invocation and returns its process exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if err := printUsage(stdout); err != nil {
			return 1
		}
		return 0
	}

	switch args[0] {
	case "ingest":
		return runIngest(args[1:], stdin, stdout, stderr)
	case "activity":
		return runActivity(args[1:], stdin, stderr)
	case "usage":
		return runUsage(args[1:], stderr)
	case "codex-notify":
		return runCodexNotify(ctx, args[1:], stderr)
	case "import":
		return runImport(args[1:], stdin, stderr)
	case "relay":
		return runRelay(ctx, args[1:], stderr)
	case "service":
		return runService(args[1:], stdout, stderr)
	case "pi":
		return runPi(args[1:], stdout, stderr)
	case "tui":
		return runTUI(ctx, args[1:], stdin, stdout, stderr)
	case "gfx":
		return runGFX(ctx, args[1:], stderr)
	case "preview":
		return runPreview(args[1:], stderr)
	case "version", "--version", "-version":
		if _, err := fmt.Fprintf(stdout, "llm-status %s (commit %s, built %s)\n", Version, Commit, Date); err != nil {
			return 1
		}
		return 0
	case "help", "--help", "-h":
		if err := printUsage(stdout); err != nil {
			return 1
		}
		return 0
	default:
		if _, err := fmt.Fprintf(stderr, "llm-status: unknown command %q\n\n", args[0]); err != nil {
			return 1
		}
		if err := printUsage(stderr); err != nil {
			return 1
		}
		return 2
	}
}

func runIngest(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	logger := logging.New(stderr, "ingest")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 1
	}
	flags := newCommandFlagSet("ingest", "Usage: llm-status ingest [--state-dir DIR]", stderr)
	stateDir := flags.String("state-dir", defaultDir, "directory used for sanitized snapshots")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 2
	}
	store, err := state.New(*stateDir)
	if err != nil {
		logger.Error().Err(err).Msg("open state store")
		return 1
	}
	_, err = ingest.Run(stdin, stdout, store, time.Now())
	if err != nil {
		logger.Error().Err(err).Msg("ingest")
		return 1
	}
	return 0
}

// runActivity records a Claude Code hook event (Notification, PreToolUse,
// Stop, UserPromptSubmit) as a working/idle/waiting-approval indicator. It
// always exits 0: some of these hook events (PreToolUse) can block the tool
// call if the hook exits non-zero, and this side channel must never do that.
func runActivity(args []string, stdin io.Reader, stderr io.Writer) int {
	logger := logging.New(stderr, "activity")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 0
	}
	flags := newCommandFlagSet("activity", "Usage: llm-status activity [--state-dir DIR]", stderr)
	stateDir := flags.String("state-dir", defaultDir, "directory used for sanitized snapshots")
	if _, parsed := parseCommandFlags(flags, args); !parsed {
		return 0
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 0
	}
	store, err := state.New(*stateDir)
	if err != nil {
		logger.Error().Err(err).Msg("open state store")
		return 0
	}
	_, _, err = activity.Run(stdin, store, time.Now())
	if err != nil {
		logger.Error().Err(err).Msg("record activity")
		return 0
	}
	return 0
}

// runUsage manually merges the two rate-limit percentages into the target
// session's stored snapshot, for interfaces (this project has only ever
// seen the VS Code extension chat panel) where statusLine never fires so
// llm-status never sees real numbers on its own.
func runUsage(args []string, stderr io.Writer) int {
	logger := logging.New(stderr, "usage")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 1
	}
	flags := newCommandFlagSet("usage", "Usage: llm-status usage --five-hour PCT --seven-day PCT [--five-hour-reset 5h] [--seven-day-reset 168h] [--session ID] [--state-dir DIR]", stderr)
	fiveHour := flags.Float64("five-hour", -1, "5-hour rate limit used percentage (0-100), required")
	sevenDay := flags.Float64("seven-day", -1, "7-day rate limit used percentage (0-100), required")
	fiveHourReset := flags.Duration("five-hour-reset", 5*time.Hour, "time until the 5-hour window resets")
	sevenDayReset := flags.Duration("seven-day-reset", 7*24*time.Hour, "time until the 7-day window resets")
	sessionID := flags.String("session", "", "session ID to update (defaults to the most recently updated session)")
	stateDir := flags.String("state-dir", defaultDir, "directory used for sanitized snapshots")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 2
	}
	if *fiveHour < 0 || *sevenDay < 0 {
		logger.Error().Msg("--five-hour and --seven-day are required")
		return 2
	}
	if *fiveHourReset <= 0 || *sevenDayReset <= 0 {
		logger.Error().Msg("--five-hour-reset and --seven-day-reset must be positive")
		return 2
	}
	store, err := state.New(*stateDir)
	if err != nil {
		logger.Error().Err(err).Msg("open state store")
		return 1
	}
	_, err = usage.Run(store, *sessionID, *fiveHour, *sevenDay, *fiveHourReset, *sevenDayReset, time.Now())
	if err != nil {
		logger.Error().Err(err).Msg("usage")
		return 1
	}
	return 0
}

func runImport(args []string, stdin io.Reader, stderr io.Writer) int {
	logger := logging.New(stderr, "import")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 1
	}
	flags := newCommandFlagSet("import", "Usage: llm-status import [--state-dir DIR]", stderr)
	stateDir := flags.String("state-dir", defaultDir, "directory used for sanitized snapshots")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 2
	}
	store, err := state.New(*stateDir)
	if err != nil {
		logger.Error().Err(err).Msg("open state store")
		return 1
	}
	snapshot, err := state.DecodeSnapshot(stdin)
	if err != nil {
		logger.Error().Err(err).Msg("decode snapshot")
		return 1
	}
	if err := store.Save(snapshot); err != nil {
		logger.Error().Err(err).Msg("save snapshot")
		return 1
	}
	return 0
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runCodexNotify(ctx context.Context, args []string, stderr io.Writer) int {
	logger := logging.New(stderr, "codex-notify")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 1
	}
	defaultCodexHome, err := codex.DefaultHome()
	if err != nil {
		logger.Error().Err(err).Msg("resolve codex home")
		return 1
	}
	flags := newCommandFlagSet("codex-notify", "Usage: llm-status codex-notify [flags] NOTIFICATION_JSON", stderr)
	stateDir := flags.String("state-dir", defaultDir, "directory used for sanitized snapshots")
	codexHome := flags.String("codex-home", defaultCodexHome, "Codex home containing session rollouts")
	forward := flags.String("forward", "", "existing notifier executable to preserve")
	var forwardArgs stringList
	flags.Var(&forwardArgs, "forward-arg", "argument for the existing notifier (repeatable)")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 1 {
		logger.Error().Msg("expected one Codex notification JSON argument")
		return 2
	}
	rawNotification := flags.Arg(0)
	if *forward != "" {
		if forwardErr := forwardNotification(ctx, *forward, forwardArgs, rawNotification); forwardErr != nil {
			logger.Warn().Err(forwardErr).Msg("forward codex notification")
		}
	}
	notification, err := codex.DecodeNotification(rawNotification)
	if err != nil {
		logger.Error().Err(err).Msg("decode notification")
		return 1
	}
	snapshot, err := codex.SnapshotFromNotification(notification, *codexHome, time.Now())
	if err != nil {
		logger.Error().Err(err).Msg("build snapshot from notification")
		return 1
	}
	store, err := state.New(*stateDir)
	if err != nil {
		logger.Error().Err(err).Msg("open state store")
		return 1
	}
	if err := store.Save(snapshot); err != nil {
		logger.Error().Err(err).Msg("save snapshot")
		return 1
	}
	return 0
}

func runRelay(ctx context.Context, args []string, stderr io.Writer) int {
	logger := logging.New(stderr, "relay")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 1
	}
	options, exitCode, parsed := parseRelayOptions(args, stderr, defaultDir)
	if !parsed {
		return exitCode
	}

	store, err := state.New(options.stateDir)
	if err != nil {
		logger.Error().Err(err).Msg("open state store")
		return 1
	}
	logOutput, file, err := openRelayLog(options.logFile, stderr)
	if err != nil {
		logger.Error().Err(err).Msg("open log file")
		return 1
	}
	if file != nil {
		defer func() {
			if closeErr := file.Close(); closeErr != nil {
				logger.Error().Err(closeErr).Msg("close log file")
			}
		}()
	}
	relayLogger := logging.New(logOutput, "relay")
	worker, err := relay.New(store, func(sendCtx context.Context, snapshot model.Snapshot) error {
		return mirror.SSH(sendCtx, options.mirrorSSH, options.remoteBinary, snapshot)
	}, relayLogger)
	if err != nil {
		relayLogger.Error().Err(err).Msg("start relay")
		return 1
	}

	ticker := time.NewTicker(options.refresh)
	defer ticker.Stop()
	for {
		syncErr := worker.Sync(ctx)
		if options.once {
			if syncErr != nil {
				return 1
			}
			return 0
		}
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
	}
}

type relayOptions struct {
	stateDir     string
	mirrorSSH    string
	remoteBinary string
	refresh      time.Duration
	once         bool
	logFile      string
}

func parseRelayOptions(args []string, stderr io.Writer, defaultDir string) (relayOptions, int, bool) {
	var options relayOptions
	logger := logging.New(stderr, "relay")
	flags := newCommandFlagSet("relay", "Usage: llm-status relay --mirror-ssh HOST [--refresh 1s] [--once] [--log-file FILE]", stderr)
	stateDir := flags.String("state-dir", defaultDir, "directory containing sanitized snapshots")
	mirrorSSH := flags.String("mirror-ssh", "", "SSH host that receives sanitized snapshots")
	remoteBinary := flags.String("remote-bin", mirror.DefaultRemoteBinary, "llm-status binary on the SSH mirror")
	refresh := flags.Duration("refresh", time.Second, "interval between local snapshot checks")
	once := flags.Bool("once", false, "send pending snapshots once and exit")
	logFile := flags.String("log-file", "", "append relay diagnostics to this file")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return options, exitCode, false
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return options, 2, false
	}
	if strings.TrimSpace(*mirrorSSH) == "" {
		logger.Error().Msg("--mirror-ssh is required")
		return options, 2, false
	}
	if *refresh < 100*time.Millisecond {
		logger.Error().Msg("--refresh must be at least 100ms")
		return options, 2, false
	}
	options = relayOptions{stateDir: *stateDir, mirrorSSH: *mirrorSSH, remoteBinary: *remoteBinary, refresh: *refresh, once: *once, logFile: *logFile}
	return options, 0, true
}

func openRelayLog(path string, stderr io.Writer) (io.Writer, *os.File, error) {
	if strings.TrimSpace(path) == "" {
		return stderr, nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}
	// #nosec G304 -- --log-file deliberately accepts an operator-selected path.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	// File comes first because MultiWriter stops on the first error. A service
	// may have no usable console, but its durable file must still receive logs.
	return io.MultiWriter(file, stderr), file, nil
}

func forwardNotification(ctx context.Context, program string, args []string, payload string) error {
	forwardCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	commandArgs := append(append([]string{}, args...), payload)
	// #nosec G204 -- the notifier is explicitly user-configured and argv is not shell-expanded.
	command := exec.CommandContext(forwardCtx, program, commandArgs...)
	// Codex fires this from its own background process; a console window
	// for the forwarded notifier would just flash on Windows.
	winconsole.SuppressChildConsole(command)
	stderr := limitio.NewBuffer(limitio.DiagnosticLimit)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("forward Codex notification: %w: %s", err, detail)
		}
		return fmt.Errorf("forward Codex notification: %w", err)
	}
	return nil
}

func runTUI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	logger := logging.New(stderr, "tui")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 1
	}
	flags := newCommandFlagSet("tui", "Usage: llm-status tui [--state-dir DIR] [--session ID] [--refresh 1s] [--stale-after 15s] [--inline]", stderr)
	stateDir := flags.String("state-dir", defaultDir, "directory containing sanitized snapshots")
	sessionID := flags.String("session", "", "initial session ID (defaults to most recent)")
	refresh := flags.Duration("refresh", time.Second, "dashboard refresh interval")
	staleAfter := flags.Duration("stale-after", 15*time.Second, "age at which a snapshot is marked stale")
	inline := flags.Bool("inline", false, "render without the terminal alternate screen")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 2
	}
	if *refresh < 250*time.Millisecond {
		logger.Error().Msg("--refresh must be at least 250ms")
		return 2
	}
	if *staleAfter <= 0 {
		logger.Error().Msg("--stale-after must be greater than zero")
		return 2
	}
	store, err := state.New(*stateDir)
	if err != nil {
		logger.Error().Err(err).Msg("open state store")
		return 1
	}
	config := dashboard.Config{
		RefreshInterval: *refresh,
		StaleAfter:      *staleAfter,
		InitialSession:  strings.TrimSpace(*sessionID),
		Inline:          *inline,
	}
	if err := dashboard.Run(ctx, stdin, stdout, store, systeminfo.NewReader("/"), config); err != nil {
		logger.Error().Err(err).Msg("tui")
		return 1
	}
	return 0
}

func runGFX(ctx context.Context, args []string, stderr io.Writer) int {
	logger := logging.New(stderr, "gfx")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 1
	}
	flags := newCommandFlagSet("gfx", "Usage: llm-status gfx [--state-dir DIR] [--refresh 66ms] [--framebuffer /dev/fb0] [--tty /dev/tty1] [--touch-device /dev/input/event0]", stderr)
	stateDir := flags.String("state-dir", defaultDir, "directory containing sanitized snapshots")
	refresh := flags.Duration("refresh", time.Second/15, "frame refresh interval (default ~15fps)")
	staleAfter := flags.Duration("stale-after", 15*time.Second, "age at which a snapshot is marked stale")
	framebufferPath := flags.String("framebuffer", "/dev/fb0", "Linux framebuffer device")
	ttyPath := flags.String("tty", "/dev/tty1", "virtual console switched to graphics mode")
	touchDevice := flags.String("touch-device", "/dev/input/event0", "evdev device for the touchscreen (empty disables touch feedback)")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 2
	}
	if *refresh < 20*time.Millisecond {
		logger.Error().Msg("--refresh must be at least 20ms")
		return 2
	}
	if *staleAfter <= 0 {
		logger.Error().Msg("--stale-after must be greater than zero")
		return 2
	}
	store, err := state.New(*stateDir)
	if err != nil {
		logger.Error().Err(err).Msg("open state store")
		return 1
	}
	renderer, err := pixelui.NewRenderer()
	if err != nil {
		logger.Error().Err(err).Msg("create renderer")
		return 1
	}
	screen, err := framebuffer.Open(*framebufferPath, *ttyPath)
	if err != nil {
		logger.Error().Err(err).Msg("open framebuffer")
		return 1
	}
	defer func() {
		if err := screen.Close(); err != nil {
			logger.Warn().Err(err).Msg("close display")
		}
	}()
	if size := screen.Size(); size.X != pixelui.Width || size.Y != pixelui.Height {
		logger.Error().Int("width", size.X).Int("height", size.Y).Msg("unexpected display size")
		return 1
	}
	var touches <-chan touch.Point
	if strings.TrimSpace(*touchDevice) != "" {
		opened, err := touch.Watch(ctx, *touchDevice)
		if err != nil {
			// Touch feedback is a nice-to-have; the dashboard's real job
			// (showing usage) must keep working without it.
			logger.Warn().Err(err).Msg("touch input disabled")
		} else {
			touches = opened
		}
	}

	config := pixelui.RunConfig{RefreshInterval: *refresh, StaleAfter: *staleAfter}
	if err := pixelui.Run(ctx, store, systeminfo.NewReader("/"), screen, renderer, config, touches); err != nil {
		logger.Error().Err(err).Msg("gfx")
		return 1
	}
	return 0
}

func runPreview(args []string, stderr io.Writer) int {
	logger := logging.New(stderr, "preview")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 1
	}
	flags := newCommandFlagSet("preview", "Usage: llm-status preview [--state-dir DIR] [--output dashboard.png] [--at RFC3339]", stderr)
	stateDir := flags.String("state-dir", defaultDir, "directory containing sanitized snapshots")
	outputPath := flags.String("output", "pixel-dashboard-preview.png", "PNG output path")
	at := flags.String("at", "", "render as of this RFC3339 timestamp instead of now (for sampling an animated mascot at a specific instant)")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 2
	}
	now := time.Now()
	if *at != "" {
		parsed, err := time.Parse(time.RFC3339, *at)
		if err != nil {
			logger.Error().Err(err).Msg("parse --at")
			return 2
		}
		now = parsed
	}
	store, err := state.New(*stateDir)
	if err != nil {
		logger.Error().Err(err).Msg("open state store")
		return 1
	}
	snapshots, loadErr := store.LoadAll()
	claude, codex := pixelui.LatestProviders(snapshots)
	todayInput, todayOutput := model.TodayTokenTotals(snapshots, now)
	renderer, err := pixelui.NewRenderer()
	if err != nil {
		logger.Error().Err(err).Msg("create renderer")
		return 1
	}
	frame := renderer.Render(pixelui.View{
		Claude:            claude,
		Codex:             codex,
		Now:               now,
		StaleAfter:        15 * time.Second,
		SessionCount:      len(snapshots),
		TodayInputTokens:  todayInput,
		TodayOutputTokens: todayOutput,
		LoadError:         loadErr,
	})
	file, err := os.Create(*outputPath)
	if err != nil {
		logger.Error().Err(err).Msg("create output")
		return 1
	}
	if err := png.Encode(file, frame); err != nil {
		err = errors.Join(err, file.Close())
		logger.Error().Err(err).Msg("encode png")
		return 1
	}
	if err := file.Close(); err != nil {
		logger.Error().Err(err).Msg("close output")
		return 1
	}
	return 0
}

func printUsage(w io.Writer) error {
	_, err := fmt.Fprintln(w, `AI Usage Terminal for Raspberry Pi

Usage:
  llm-status ingest [flags]       Read Claude Code statusLine JSON from stdin
  llm-status activity [flags]     Read a Claude Code hook event from stdin
  llm-status usage [flags]        Manually set 5h/7d rate-limit percentages
  llm-status codex-notify [flags] Read a Codex turn-complete notification
  llm-status import [flags]       Import one sanitized snapshot
  llm-status relay [flags]        Retry local snapshot delivery over SSH
  llm-status service <verb>       Install/remove/start/stop/status the relay as a background service
  llm-status pi install [flags]   Set up the framebuffer dashboard service on this Raspberry Pi
  llm-status gfx [flags]          Render the 800x480 framebuffer dashboard
  llm-status preview [flags]      Save one framebuffer dashboard frame as PNG
  llm-status tui [flags]          Open the full-screen dashboard
  llm-status version              Print build information

Run "llm-status <command> --help" for command flags.`)
	return err
}
