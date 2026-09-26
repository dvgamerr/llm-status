package llmproxy

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/dvgamerr/llm-status/internal/logging"
	"github.com/dvgamerr/llm-status/internal/service"
)

// defaultListen is the fallback listen address when PORT is unset.
const defaultListen = "127.0.0.1:8787"

// defaultListenAddr honors PORT (as set in .env or the process environment)
// as the default port for --listen, so a deployment can move the proxy off
// 8787 without passing --listen explicitly. --listen still overrides it.
func defaultListenAddr() string {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		return defaultListen
	}
	return "127.0.0.1:" + port
}

// logFormatFromEnv mirrors logging.New's own LOG_FORMAT check, so the
// startup log line reports the format actually in effect.
func logFormatFromEnv() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("LOG_FORMAT")), "text") {
		return "text"
	}
	return "json"
}

// ServiceName identifies the background service across all three platforms:
// the Windows Service name, the systemd --user unit name (sans ".service"),
// and the launchd LaunchAgent label.
const ServiceName = "llm-proxy"

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

func newCommandFlagSet(name, usage string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprintln(stderr, usage) }
	return flags
}

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
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if err := printUsage(stdout); err != nil {
			return 1
		}
		return 0
	}
	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], stderr)
	case "service":
		return runService(args[1:], stdout, stderr)
	case "version", "--version", "-version":
		if _, err := fmt.Fprintln(stdout, "llm-proxy dev"); err != nil {
			return 1
		}
		return 0
	case "help", "--help", "-h":
		if err := printUsage(stdout); err != nil {
			return 1
		}
		return 0
	default:
		if _, err := fmt.Fprintf(stderr, "llm-proxy: unknown command %q\n\n", args[0]); err != nil {
			return 1
		}
		if err := printUsage(stderr); err != nil {
			return 1
		}
		return 2
	}
}

func runServe(ctx context.Context, args []string, stderr io.Writer) int {
	logger := logging.New(stderr, "serve")
	defaultExamples, err := defaultExamplesDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve default examples directory")
		return 1
	}
	flags := newCommandFlagSet("serve", "Usage: llm-proxy serve [--listen 127.0.0.1:8787] [--anthropic-upstream URL] [--openai-upstream URL] [--log-file FILE] [--examples-dir DIR]", stderr)
	listen := flags.String("listen", defaultListenAddr(), "address the proxy listens on (defaults to PORT env var if set)")
	anthropicUpstream := flags.String("anthropic-upstream", DefaultUpstreams["/anthropic"], "upstream for requests under /anthropic")
	openaiUpstream := flags.String("openai-upstream", DefaultUpstreams["/openai"], "upstream for requests under /openai")
	logFile := flags.String("log-file", "", "append proxy diagnostics to this file")
	examplesDir := flags.String("examples-dir", defaultExamples, "save a sanitized request/response example per route here (empty disables)")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 2
	}

	logOutput, file, err := openLogFile(*logFile, stderr)
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
	serveLogger := logging.New(logOutput, "serve")
	serveLogger.Info().
		Str("port_env", os.Getenv("PORT")).
		Str("log_level", zerolog.GlobalLevel().String()).
		Str("log_format", logFormatFromEnv()).
		Msg("env config")

	host, _, splitErr := net.SplitHostPort(*listen)
	if splitErr == nil && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		serveLogger.Warn().Str("listen", *listen).Msg("listening on a non-loopback address exposes upstream API credentials forwarded through this proxy to your network")
	}

	handler, err := NewHandler(map[string]string{
		"/anthropic": *anthropicUpstream,
		"/openai":    *openaiUpstream,
	}, serveLogger, *examplesDir)
	if err != nil {
		logger.Error().Err(err).Msg("build proxy handler")
		return 1
	}
	if *examplesDir != "" {
		serveLogger.Info().Str("examples_dir", *examplesDir).Msg("saving sanitized request/response examples per route")
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	serveLogger.Info().Str("listen", *listen).Msg("llm-proxy listening")

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			serveLogger.Error().Err(err).Msg("shutdown")
			return 1
		}
		return 0
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveLogger.Error().Err(err).Msg("serve")
			return 1
		}
		return 0
	}
}

func openLogFile(path string, stderr io.Writer) (io.Writer, *os.File, error) {
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
	return io.MultiWriter(file, stderr), file, nil
}

func runService(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if _, err := fmt.Fprintln(stderr, "Usage: llm-proxy service <install|remove|start|stop|status> [flags]"); err != nil {
			return 1
		}
		return 2
	}
	switch args[0] {
	case "install":
		return runServiceInstall(args[1:], stderr)
	case "remove":
		logger := logging.New(stderr, "service remove")
		if err := serviceRemove(ServiceName); err != nil {
			logger.Error().Err(err).Msg("remove service")
			return 1
		}
		if _, err := fmt.Fprintf(stdout, "removed %s\n", ServiceName); err != nil {
			return 1
		}
		return 0
	case "start":
		return runServiceControl(serviceStart, serviceStatus, "start", stdout, stderr)
	case "stop":
		return runServiceControl(serviceStop, serviceStatus, "stop", stdout, stderr)
	case "status":
		state, err := serviceStatus(ServiceName)
		if err != nil {
			logger := logging.New(stderr, "service status")
			logger.Error().Err(err).Msg("service status")
			return 1
		}
		if _, err := fmt.Fprintf(stdout, "%s: %s\n", ServiceName, state); err != nil {
			return 1
		}
		return 0
	case "help", "--help", "-h":
		if _, err := fmt.Fprintln(stdout, "Usage: llm-proxy service <install|remove|start|stop|status> [flags]"); err != nil {
			return 1
		}
		return 0
	default:
		logger := logging.New(stderr, "service")
		logger.Error().Str("subcommand", args[0]).Msg("unknown subcommand")
		return 2
	}
}

func runServiceInstall(args []string, stderr io.Writer) int {
	logger := logging.New(stderr, "service install")
	defaultLog, err := defaultLogPath()
	if err != nil {
		logger.Error().Err(err).Msg("resolve default log path")
		return 1
	}
	defaultExamples, err := defaultExamplesDir()
	if err != nil {
		logger.Error().Err(err).Msg("resolve default examples directory")
		return 1
	}
	flags := newCommandFlagSet("service install", "Usage: llm-proxy service install [--listen 127.0.0.1:8787] [--anthropic-upstream URL] [--openai-upstream URL] [--log-file FILE] [--examples-dir DIR]", stderr)
	listen := flags.String("listen", defaultListenAddr(), "address the proxy listens on (defaults to PORT env var if set)")
	anthropicUpstream := flags.String("anthropic-upstream", DefaultUpstreams["/anthropic"], "upstream for requests under /anthropic")
	openaiUpstream := flags.String("openai-upstream", DefaultUpstreams["/openai"], "upstream for requests under /openai")
	logFile := flags.String("log-file", defaultLog, "proxy diagnostics log file")
	examplesDir := flags.String("examples-dir", defaultExamples, "save a sanitized request/response example per route here (empty disables)")
	if exitCode, parsed := parseCommandFlags(flags, args); !parsed {
		return exitCode
	}
	if flags.NArg() != 0 {
		logger.Error().Msg("unexpected positional arguments")
		return 2
	}

	cfg := service.Config{
		Name:        ServiceName,
		DisplayName: "LLM Proxy",
		Description: "Local reverse proxy that forwards Claude and Codex API traffic to their real upstreams.",
		Args: []string{
			"serve",
			"--listen", *listen,
			"--anthropic-upstream", *anthropicUpstream,
			"--openai-upstream", *openaiUpstream,
			"--log-file", *logFile,
			"--examples-dir", *examplesDir,
		},
	}
	if err := serviceInstall(cfg); err != nil {
		logger.Error().Err(err).Msg("install service")
		return 1
	}
	if _, err := fmt.Fprintf(stderr, "installed and started %s (log: %s)\n", ServiceName, *logFile); err != nil {
		return 1
	}
	return 0
}

func runServiceControl(action func(string) error, status func(string) (service.State, error), verb string, stdout, stderr io.Writer) int {
	if err := action(ServiceName); err != nil {
		logger := logging.New(stderr, "service "+verb)
		logger.Error().Err(err).Msg("service " + verb)
		return 1
	}
	state, err := status(ServiceName)
	if err != nil {
		if _, writeErr := fmt.Fprintf(stdout, "%s: %s requested\n", ServiceName, verb); writeErr != nil {
			return 1
		}
		return 0
	}
	if _, err := fmt.Fprintf(stdout, "%s: %s\n", ServiceName, state); err != nil {
		return 1
	}
	return 0
}

func defaultLogPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "llm-proxy", "llm-proxy.log"), nil
}

func defaultExamplesDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "llm-proxy", "examples"), nil
}

func printUsage(w io.Writer) error {
	_, err := fmt.Fprintln(w, `Local reverse proxy for Claude and Codex API traffic

Usage:
  llm-proxy serve [flags]     Run the proxy in the foreground
  llm-proxy service <verb>    Install/remove/start/stop/status the proxy as a background service
  llm-proxy version           Print build information

Run "llm-proxy <command> --help" for command flags.`)
	return err
}
