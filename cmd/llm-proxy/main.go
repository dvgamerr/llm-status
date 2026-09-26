// Command llm-proxy is a local reverse proxy that forwards Claude and Codex
// API traffic to their real upstreams.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dvgamerr/llm-status/internal/envfile"
	"github.com/dvgamerr/llm-status/internal/llmproxy"
	"github.com/dvgamerr/llm-status/internal/service"
	"github.com/dvgamerr/llm-status/internal/winconsole"
)

func main() {
	// Loaded first so PORT/LOG_LEVEL/LOG_FORMAT from a local .env reach every
	// path below, including the Windows Service branch. Never overrides a
	// variable already set in the real environment.
	_ = envfile.LoadDefault()

	// Checked before anything else: the Windows Service Control Manager
	// expects a service process to call StartServiceCtrlDispatcher almost
	// immediately, so this can't wait for flag parsing or any other CLI
	// setup. IsWindowsService is always false outside Windows.
	if service.IsWindowsService() {
		// A service has no interactive user, so any console the Service
		// Control Manager handed this process is just a window flashing
		// on someone's desktop at boot and on every recovery restart.
		winconsole.HideServiceConsole()
		err := service.RunAsService(llmproxy.ServiceName, func(ctx context.Context) error {
			if code := llmproxy.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); code != 0 {
				return fmt.Errorf("llm-proxy exited with code %d", code)
			}
			return nil
		})
		if err != nil {
			os.Exit(1)
		}
		return
	}

	winconsole.HideDetachedConsole()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(llmproxy.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
