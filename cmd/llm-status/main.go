// Command llm-status ingests provider usage and renders local dashboards.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dvgamerr/llm-status/internal/app"
	"github.com/dvgamerr/llm-status/internal/envfile"
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
		err := service.RunAsService(app.RelayServiceName, func(ctx context.Context) error {
			if code := app.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); code != 0 {
				return fmt.Errorf("llm-status exited with code %d", code)
			}
			return nil
		})
		if err != nil {
			os.Exit(1)
		}
		return
	}

	// Claude Code hooks fired from the VS Code extension host launch this
	// binary from a parent that has no console of its own, so Windows
	// allocates a fresh window per hook event. Hide those; a console
	// inherited from a real shell stays visible.
	winconsole.HideDetachedConsole()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(app.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
