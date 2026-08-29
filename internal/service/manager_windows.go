//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type serviceManager interface {
	Disconnect() error
	OpenService(string) (managedService, error)
	CreateService(string, string, mgr.Config, ...string) (managedService, error)
}

type managedService interface {
	Close() error
	UpdateConfig(mgr.Config) error
	Query() (svc.Status, error)
	Control(svc.Cmd) (svc.Status, error)
	Delete() error
	Start(...string) error
	SetRecoveryActions([]mgr.RecoveryAction, uint32) error
	SetRecoveryActionsOnNonCrashFailures(bool) error
}

type nativeServiceManager struct{ manager *mgr.Mgr }

func (manager *nativeServiceManager) Disconnect() error { return manager.manager.Disconnect() }
func (manager *nativeServiceManager) OpenService(name string) (managedService, error) {
	service, err := manager.manager.OpenService(name)
	if err != nil {
		return nil, err
	}
	return service, nil
}
func (manager *nativeServiceManager) CreateService(name, executable string, config mgr.Config, args ...string) (managedService, error) {
	service, err := manager.manager.CreateService(name, executable, config, args...)
	if err != nil {
		return nil, err
	}
	return service, nil
}

var connectServiceManager = func() (serviceManager, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	return &nativeServiceManager{manager: manager}, nil
}

// Install registers cfg as a Windows Service (creating it if new, updating
// its binary path/description in place if it already exists) and starts it.
// StartAutomatic means it comes back on its own after a reboot, matching
// the always-on relay behavior the Scheduled Task approach used to give.
func Install(cfg Config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	m, err := connectServiceManager()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	binaryPath := serviceBinaryPath(exePath, cfg.Args)
	s, err := m.OpenService(cfg.Name)
	if err == nil {
		defer func() { _ = s.Close() }()
		update := mgr.Config{
			ServiceType:    windows.SERVICE_NO_CHANGE,
			StartType:      mgr.StartAutomatic,
			ErrorControl:   windows.SERVICE_NO_CHANGE,
			DisplayName:    cfg.DisplayName,
			Description:    cfg.Description,
			BinaryPathName: binaryPath,
		}
		if updateErr := s.UpdateConfig(update); updateErr != nil {
			return fmt.Errorf("update service config: %w", updateErr)
		}
		// UpdateConfig only changes what SCM will launch next time — a
		// currently running instance keeps executing the old binary path
		// in memory. Restart so a re-install (e.g. after upgrading the
		// binary) actually picks up the change instead of silently doing
		// nothing until the next reboot.
		status, queryErr := s.Query()
		if queryErr != nil {
			return fmt.Errorf("query service before update restart: %w", queryErr)
		}
		if status.State != svc.Stopped {
			if _, controlErr := s.Control(svc.Stop); controlErr != nil {
				return fmt.Errorf("stop running service before restart: %w", controlErr)
			}
			if waitErr := waitForState(s, svc.Stopped, 20*time.Second); waitErr != nil {
				return fmt.Errorf("wait for service to stop before restart: %w", waitErr)
			}
		}
	} else if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		s, err = m.CreateService(cfg.Name, exePath, mgr.Config{
			DisplayName:  cfg.DisplayName,
			Description:  cfg.Description,
			StartType:    mgr.StartAutomatic,
			ErrorControl: mgr.ErrorNormal,
			// Stated rather than left to the default so it is obvious this
			// is never SERVICE_INTERACTIVE_PROCESS: the relay must not be
			// able to put a console on the logged-in user's desktop.
			ServiceType: windows.SERVICE_WIN32_OWN_PROCESS,
		}, cfg.Args...)
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		defer func() { _ = s.Close() }()
	} else {
		return fmt.Errorf("open existing service: %w", err)
	}

	if err := configureRecovery(s); err != nil {
		return err
	}

	if err := doStart(s); err != nil {
		return err
	}
	return waitForState(s, svc.Running, 20*time.Second)
}

// Remove stops name if running and deletes its service registration.
// Remove stops and deletes a Windows Service registration.
func Remove(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	m, err := connectServiceManager()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %q is not installed: %w", name, err)
	}
	defer func() { _ = s.Close() }()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service before removal: %w", err)
	}
	if status.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop service before removal: %w", err)
		}
		if err := waitForState(s, svc.Stopped, 20*time.Second); err != nil {
			return fmt.Errorf("wait for service to stop: %w", err)
		}
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

// Start starts an installed Windows Service.
func Start(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	m, err := connectServiceManager()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %q is not installed: %w", name, err)
	}
	defer func() { _ = s.Close() }()
	if err := doStart(s); err != nil {
		return err
	}
	return waitForState(s, svc.Running, 20*time.Second)
}

// Stop stops an installed Windows Service.
func Stop(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	m, err := connectServiceManager()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %q is not installed: %w", name, err)
	}
	defer func() { _ = s.Close() }()
	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service status: %w", err)
	}
	if status.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	return waitForState(s, svc.Stopped, 20*time.Second)
}

// Status queries a Windows Service registration and running state.
func Status(name string) (State, error) {
	if err := validateName(name); err != nil {
		return StateNotInstalled, err
	}
	m, err := connectServiceManager()
	if err != nil {
		return StateNotInstalled, fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return StateNotInstalled, nil
		}
		return StateNotInstalled, fmt.Errorf("open service: %w", err)
	}
	defer func() { _ = s.Close() }()
	status, err := s.Query()
	if err != nil {
		return StateNotInstalled, fmt.Errorf("query service status: %w", err)
	}
	if status.State == svc.Running {
		return StateRunning, nil
	}
	return StateStopped, nil
}

func configureRecovery(s managedService) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, 24*60*60); err != nil {
		return fmt.Errorf("configure service recovery actions: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("enable recovery for non-crash failures: %w", err)
	}
	return nil
}

func doStart(s managedService) error {
	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service status: %w", err)
	}
	if status.State == svc.Running {
		return nil
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

func waitForState(s managedService, target svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status, err := s.Query()
		if err != nil {
			return fmt.Errorf("query service status: %w", err)
		}
		if status.State == target {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for service state %d", target)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func serviceBinaryPath(exePath string, args []string) string {
	quoted := make([]string, 0, len(args)+1)
	quoted = append(quoted, syscall.EscapeArg(exePath))
	for _, arg := range args {
		quoted = append(quoted, syscall.EscapeArg(arg))
	}
	return strings.Join(quoted, " ")
}
