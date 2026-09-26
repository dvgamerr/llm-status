package app

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/dvgamerr/llm-status/internal/logging"
	"github.com/dvgamerr/llm-status/internal/mirror"
	"github.com/dvgamerr/llm-status/internal/service"
	"github.com/dvgamerr/llm-status/internal/state"
)

// RelayServiceName identifies the background service across all three
// platforms: the Windows Service name, the systemd --user unit name (sans
// ".service"), and the launchd LaunchAgent label.
const RelayServiceName = "llm-status-relay"

// These are overridden in tests so runService/runServiceInstall can be
// exercised without actually installing/starting/stopping a real
// systemd/Windows service.
var (
	serviceInstall = service.Install
	serviceRemove  = service.Remove
	serviceStart   = service.Start
	serviceStop    = service.Stop
	serviceStatus  = service.Status
)

// runService is the one `llm-status service <verb>` entry point for
// Windows/Linux/macOS alike — see internal/service for how each OS's own
// service manager is driven underneath.
func runService(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if _, err := fmt.Fprintln(stderr, "Usage: llm-status service <install|remove|start|stop|status> [flags]"); err != nil {
			return 1
		}
		return 2
	}
	switch args[0] {
	case "install":
		return runServiceInstall(args[1:], stderr)
	case "remove":
		logger := logging.New(stderr, "service remove")
		if err := serviceRemove(RelayServiceName); err != nil {
			logger.Error().Err(err).Msg("remove service")
			return 1
		}
		if _, err := fmt.Fprintf(stdout, "removed %s\n", RelayServiceName); err != nil {
			return 1
		}
		return 0
	case "start":
		return runServiceControl(serviceStart, serviceStatus, "start", stdout, stderr)
	case "stop":
		return runServiceControl(serviceStop, serviceStatus, "stop", stdout, stderr)
	case "status":
		state, err := serviceStatus(RelayServiceName)
		if err != nil {
			logger := logging.New(stderr, "service status")
			logger.Error().Err(err).Msg("service status")
			return 1
		}
		if _, err := fmt.Fprintf(stdout, "%s: %s\n", RelayServiceName, state); err != nil {
			return 1
		}
		return 0
	case "help", "--help", "-h":
		if _, err := fmt.Fprintln(stdout, "Usage: llm-status service <install|remove|start|stop|status> [flags]"); err != nil {
			return 1
		}
		return 0
	default:
		logger := logging.New(stderr, "service")
		logger.Error().Str("subcommand", args[0]).Msg("unknown subcommand")
		return 2
	}
}

// runServiceInstall registers (or reconfigures, if already installed) the
// relay as a background service and starts it — the cross-platform
// replacement for install-windows.ps1's Scheduled Task registration.
func runServiceInstall(args []string, stderr io.Writer) int {
	logger := logging.New(stderr, "service install")
	defaultDir, err := state.DefaultDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve state directory")
		return 1
	}
	flags := newCommandFlagSet("service install", "Usage: llm-status service install --mirror-ssh HOST [--remote-bin PATH] [--refresh 1s] [--state-dir DIR] [--log-file FILE]", stderr)
	mirrorSSH := flags.String("mirror-ssh", "", "SSH host that receives sanitized snapshots")
	remoteBinary := flags.String("remote-bin", mirror.DefaultRemoteBinary, "llm-status binary on the SSH mirror")
	refresh := flags.Duration("refresh", time.Second, "interval between local snapshot checks")
	stateDir := flags.String("state-dir", defaultDir, "directory containing sanitized snapshots")
	logFile := flags.String("log-file", filepath.Join(defaultDir, "relay.log"), "relay diagnostics log file")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 2
	}
	if strings.TrimSpace(*mirrorSSH) == "" {
		logger.Error().Msg("--mirror-ssh is required")
		return 2
	}
	if *refresh < 100*time.Millisecond {
		logger.Error().Msg("--refresh must be at least 100ms")
		return 2
	}

	cfg := service.Config{
		Name:        RelayServiceName,
		DisplayName: "Claude Status Relay",
		Description: "Mirrors sanitized Claude/Codex usage snapshots to a remote dashboard over SSH.",
		Args: []string{
			"relay",
			"--mirror-ssh", *mirrorSSH,
			"--remote-bin", *remoteBinary,
			"--refresh", refresh.String(),
			"--state-dir", *stateDir,
			"--log-file", *logFile,
		},
	}
	if err := serviceInstall(cfg); err != nil {
		logger.Error().Err(err).Msg("install service")
		return 1
	}
	if _, err := fmt.Fprintf(stderr, "installed and started %s (log: %s)\n", RelayServiceName, *logFile); err != nil {
		return 1
	}
	return 0
}

func runServiceControl(action func(string) error, status func(string) (service.State, error), verb string, stdout, stderr io.Writer) int {
	if err := action(RelayServiceName); err != nil {
		logger := logging.New(stderr, "service "+verb)
		logger.Error().Err(err).Msg("service " + verb)
		return 1
	}
	state, err := status(RelayServiceName)
	if err != nil {
		if _, writeErr := fmt.Fprintf(stdout, "%s: %s requested\n", RelayServiceName, verb); writeErr != nil {
			return 1
		}
		return 0
	}
	if _, err := fmt.Fprintf(stdout, "%s: %s\n", RelayServiceName, state); err != nil {
		return 1
	}
	return 0
}
