package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dvgamerr/llm-status/internal/atomicfile"
	"github.com/dvgamerr/llm-status/internal/logging"
	"github.com/dvgamerr/llm-status/internal/systemdunit"
)

// tty1UnitName is the systemd unit that owns /dev/tty1 on the Pi — see
// configs/llm-status-tty1.service, which `pi install` supersedes with a
// version whose paths/user are resolved at install time instead of
// hardcoded to /home/pi.
const tty1UnitName = "llm-status-tty1.service"

// systemdUnitDir and geteuid are overridden in tests so runPiInstall's
// root-owned branch can be exercised against a temp directory instead of
// the real /etc/systemd/system, and without needing the test process to
// actually run as root.
var (
	systemdUnitDir = "/etc/systemd/system"
	geteuid        = os.Geteuid
	goos           = runtime.GOOS
)

// runPi is the display-side counterpart to `service` (which sets up the
// relay on the machine running Claude Code/Codex): `llm-status pi
// install` sets up the framebuffer dashboard on the Raspberry Pi itself.
func runPi(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if _, err := fmt.Fprintln(stderr, "Usage: llm-status pi <install> [flags]"); err != nil {
			return 1
		}
		return 2
	}
	switch args[0] {
	case "install":
		return runPiInstall(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		if _, err := fmt.Fprintln(stdout, "Usage: llm-status pi install [flags]"); err != nil {
			return 1
		}
		return 0
	default:
		logger := logging.New(stderr, "pi")
		logger.Error().Str("subcommand", args[0]).Msg("unknown subcommand")
		return 2
	}
}

// runPiInstall writes and enables the tty1 framebuffer service, pointing it
// at wherever this binary actually is — which lets `go install .../cmd/
// llm-status@latest` followed by `llm-status pi install` work with no
// git checkout at all, unlike the old scripts/install.sh + manual
// `sudo systemctl enable` flow.
func runPiInstall(args []string, stdout, stderr io.Writer) int {
	logger := logging.New(stderr, "pi install")
	options, exitCode, parsed := parsePiInstallOptions(args, stderr)
	if !parsed {
		return exitCode
	}
	if goos != "linux" {
		logger.Error().Msg("only supported on Linux (Raspberry Pi OS)")
		return 1
	}

	exePath, err := os.Executable()
	if err != nil {
		logger.Error().Err(err).Msg("resolve executable path")
		return 1
	}
	if resolved, resolveErr := filepath.EvalSymlinks(exePath); resolveErr == nil {
		exePath = resolved
	}

	// Writing the unit file and calling systemctl both need root; the
	// tty1 device itself is owned by the target user via SupplementaryGroups,
	// not by running the dashboard process as root.
	if geteuid() != 0 {
		return reExecWithSudo(exePath, args, stderr)
	}

	targetUser := strings.TrimSpace(options.user)
	if targetUser == "" {
		targetUser = os.Getenv("SUDO_USER")
	}
	if targetUser == "" {
		targetUser = "pi"
	}
	account, err := user.Lookup(targetUser)
	if err != nil {
		logger.Error().Err(err).Str("user", targetUser).Msg("look up user")
		return 1
	}
	primaryGroup, err := user.LookupGroupId(account.Gid)
	if err != nil {
		logger.Error().Err(err).Str("gid", account.Gid).Msg("look up primary group")
		return 1
	}

	unit, err := buildPiUnit(piUnitConfig{
		executable:  exePath,
		refresh:     options.refresh,
		framebuffer: options.framebuffer,
		tty:         options.tty,
		touchDevice: options.touchDevice,
		user:        account.Username,
		group:       primaryGroup.Name,
		home:        account.HomeDir,
	})
	if err != nil {
		logger.Error().Err(err).Msg("format systemd unit")
		return 1
	}

	unitDestination := filepath.Join(systemdUnitDir, tty1UnitName)
	if err := atomicfile.Write(unitDestination, []byte(unit), 0o644); err != nil {
		logger.Error().Err(err).Str("path", unitDestination).Msg("write unit file")
		return 1
	}
	if err := runSystemCommand("systemctl", "daemon-reload"); err != nil {
		logger.Error().Err(err).Msg("systemctl daemon-reload")
		return 1
	}
	if err := runSystemCommand("systemctl", "enable", "--now", tty1UnitName); err != nil {
		logger.Error().Err(err).Msg("systemctl enable")
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "installed and started %s (binary: %s, user: %s)\n", tty1UnitName, exePath, account.Username); err != nil {
		logger.Error().Err(err).Msg("write install confirmation")
		return 1
	}
	return 0
}

type piInstallOptions struct {
	user        string
	refresh     time.Duration
	framebuffer string
	tty         string
	touchDevice string
}

func parsePiInstallOptions(args []string, stderr io.Writer) (piInstallOptions, int, bool) {
	var options piInstallOptions
	logger := logging.New(stderr, "pi install")
	flags := newCommandFlagSet("pi install", "Usage: llm-status pi install [--user NAME] [--refresh 66ms] [--framebuffer /dev/fb0] [--tty /dev/tty1] [--touch-device /dev/input/event0]", stderr)
	userName := flags.String("user", "", "user the dashboard service runs as (default: $SUDO_USER, or the current user)")
	refresh := flags.Duration("refresh", time.Second/15, "frame refresh interval")
	framebufferPath := flags.String("framebuffer", "/dev/fb0", "Linux framebuffer device")
	ttyPath := flags.String("tty", "/dev/tty1", "virtual console switched to graphics mode")
	touchDevice := flags.String("touch-device", "/dev/input/event0", "evdev device for touch feedback (empty disables it)")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return options, exitCode, false
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return options, 2, false
	}
	if *refresh < 20*time.Millisecond {
		logger.Error().Msg("--refresh must be at least 20ms")
		return options, 2, false
	}
	options = piInstallOptions{user: *userName, refresh: *refresh, framebuffer: *framebufferPath, tty: *ttyPath, touchDevice: *touchDevice}
	return options, 0, true
}

type piUnitConfig struct {
	executable  string
	refresh     time.Duration
	framebuffer string
	tty         string
	touchDevice string
	user        string
	group       string
	home        string
}

func buildPiUnit(config piUnitConfig) (string, error) {
	execArgs := []string{config.executable, "gfx", "--refresh", config.refresh.String(), "--framebuffer", config.framebuffer, "--tty", config.tty}
	if strings.TrimSpace(config.touchDevice) != "" {
		execArgs = append(execArgs, "--touch-device", config.touchDevice)
	}
	execStart, err := systemdunit.Command(execArgs...)
	if err != nil {
		return "", fmt.Errorf("format ExecStart: %w", err)
	}
	execStartPre, err := systemdunit.Command("/usr/bin/test", "-x", config.executable)
	if err != nil {
		return "", fmt.Errorf("format ExecStartPre: %w", err)
	}
	values := make([]string, 0, 7)
	for _, value := range []string{
		"AI Status 800x480 framebuffer dashboard",
		"getty@" + filepath.Base(config.tty) + ".service",
		config.user,
		config.group,
		config.home,
		"HOME=" + config.home,
		config.tty,
	} {
		quoted, quoteErr := systemdunit.Quote(value)
		if quoteErr != nil {
			return "", quoteErr
		}
		values = append(values, quoted)
	}
	return fmt.Sprintf(`[Unit]
Description=%s
After=local-fs.target systemd-user-sessions.service
Conflicts=%s

[Service]
Type=exec
User=%s
Group=%s
SupplementaryGroups=video input
WorkingDirectory=%s
Environment=%s
ExecStartPre=%s
ExecStart=%s
Restart=always
RestartSec=2
StandardInput=tty
StandardOutput=journal
StandardError=journal
TTYPath=%s
TTYReset=yes
TTYVHangup=yes
TTYVTDisallocate=yes

[Install]
WantedBy=multi-user.target
`, values[0], values[1], values[2], values[3], values[4], values[5], execStartPre, execStart, values[6]), nil
}

// reExecWithSudo re-invokes this same subcommand under sudo so the user can
// run one command (`llm-status pi install`) instead of having to
// remember to prefix it themselves.
func reExecWithSudo(exePath string, args []string, stderr io.Writer) int {
	sudoArgs := append([]string{exePath, "pi", "install"}, args...)
	// #nosec G204 -- sudo receives an argv slice directly; no shell parses it.
	cmd := exec.Command("sudo", sudoArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		logger := logging.New(stderr, "pi install")
		logger.Error().Err(err).Msg("re-exec via sudo")
		return 1
	}
	return 0
}

func runSystemCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, detail)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
