//go:build windows

package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type fakeServiceManager struct {
	service      *fakeManagedService
	openErr      error
	createErr    error
	disconnected bool
	created      bool
}

func (manager *fakeServiceManager) Disconnect() error {
	manager.disconnected = true
	return nil
}
func (manager *fakeServiceManager) OpenService(string) (managedService, error) {
	if manager.openErr != nil {
		return nil, manager.openErr
	}
	return manager.service, nil
}
func (manager *fakeServiceManager) CreateService(string, string, mgr.Config, ...string) (managedService, error) {
	manager.created = true
	if manager.createErr != nil {
		return nil, manager.createErr
	}
	return manager.service, nil
}

type fakeManagedService struct {
	state           svc.State
	queryErr        error
	updateErr       error
	controlErr      error
	deleteErr       error
	startErr        error
	recoveryErr     error
	nonCrashErr     error
	closed          bool
	updated         bool
	controlled      bool
	deleted         bool
	started         bool
	recoveryActions int
}

func (service *fakeManagedService) Close() error {
	service.closed = true
	return nil
}
func (service *fakeManagedService) UpdateConfig(mgr.Config) error {
	service.updated = true
	return service.updateErr
}
func (service *fakeManagedService) Query() (svc.Status, error) {
	return svc.Status{State: service.state}, service.queryErr
}
func (service *fakeManagedService) Control(command svc.Cmd) (svc.Status, error) {
	service.controlled = true
	if service.controlErr != nil {
		return svc.Status{}, service.controlErr
	}
	if command == svc.Stop {
		service.state = svc.Stopped
	}
	return svc.Status{State: service.state}, nil
}
func (service *fakeManagedService) Delete() error {
	service.deleted = true
	return service.deleteErr
}
func (service *fakeManagedService) Start(...string) error {
	service.started = true
	if service.startErr == nil {
		service.state = svc.Running
	}
	return service.startErr
}
func (service *fakeManagedService) SetRecoveryActions(actions []mgr.RecoveryAction, _ uint32) error {
	service.recoveryActions = len(actions)
	return service.recoveryErr
}
func (service *fakeManagedService) SetRecoveryActionsOnNonCrashFailures(bool) error {
	return service.nonCrashErr
}

func useFakeManager(t *testing.T, manager *fakeServiceManager) {
	t.Helper()
	original := connectServiceManager
	connectServiceManager = func() (serviceManager, error) { return manager, nil }
	t.Cleanup(func() { connectServiceManager = original })
}

func validServiceConfig() Config {
	return Config{Name: "llm-status-test", DisplayName: "Test", Description: "Test service", Args: []string{"relay", "--once"}}
}

func TestServiceBinaryPathEscapesWindowsArguments(t *testing.T) {
	got := serviceBinaryPath(`C:\Program Files\Claude Status\llm-status.exe`, []string{"relay", `a b`, `quote"here`, ``})
	want := `"C:\Program Files\Claude Status\llm-status.exe" relay "a b" quote\"here ""`
	if got != want {
		t.Fatalf("serviceBinaryPath() = %q, want %q", got, want)
	}
}

func TestInstallCreatesAndUpdatesServices(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		service := &fakeManagedService{state: svc.Stopped}
		manager := &fakeServiceManager{service: service, openErr: windows.ERROR_SERVICE_DOES_NOT_EXIST}
		useFakeManager(t, manager)
		if err := Install(validServiceConfig()); err != nil {
			t.Fatal(err)
		}
		if !manager.created || !manager.disconnected || !service.started || !service.closed || service.recoveryActions != 3 {
			t.Fatalf("manager=%+v service=%+v", manager, service)
		}
	})

	t.Run("update and restart", func(t *testing.T) {
		service := &fakeManagedService{state: svc.Running}
		manager := &fakeServiceManager{service: service}
		useFakeManager(t, manager)
		if err := Install(validServiceConfig()); err != nil {
			t.Fatal(err)
		}
		if !service.updated || !service.controlled || !service.started || service.state != svc.Running {
			t.Fatalf("service=%+v", service)
		}
	})
}

func TestInstallReportsManagerAndServiceFailures(t *testing.T) {
	original := connectServiceManager
	connectServiceManager = func() (serviceManager, error) { return nil, errors.New("connect failed") }
	if err := Install(validServiceConfig()); err == nil || !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("connect error = %v", err)
	}
	connectServiceManager = original

	tests := []struct {
		name    string
		manager *fakeServiceManager
		want    string
	}{
		{"open", &fakeServiceManager{service: &fakeManagedService{}, openErr: windows.ERROR_ACCESS_DENIED}, "open existing"},
		{"create", &fakeServiceManager{service: &fakeManagedService{}, openErr: windows.ERROR_SERVICE_DOES_NOT_EXIST, createErr: errors.New("create failed")}, "create service"},
		{"update", &fakeServiceManager{service: &fakeManagedService{updateErr: errors.New("update failed")}}, "update service"},
		{"query", &fakeServiceManager{service: &fakeManagedService{queryErr: errors.New("query failed")}}, "query service"},
		{"control", &fakeServiceManager{service: &fakeManagedService{state: svc.Running, controlErr: errors.New("control failed")}}, "stop running"},
		{"recovery", &fakeServiceManager{service: &fakeManagedService{recoveryErr: errors.New("recovery failed")}}, "recovery actions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			useFakeManager(t, test.manager)
			err := Install(validServiceConfig())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Install() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRemoveStartStopAndStatus(t *testing.T) {
	t.Run("remove running", func(t *testing.T) {
		service := &fakeManagedService{state: svc.Running}
		useFakeManager(t, &fakeServiceManager{service: service})
		if err := Remove("llm-status-test"); err != nil {
			t.Fatal(err)
		}
		if !service.controlled || !service.deleted {
			t.Fatalf("service=%+v", service)
		}
	})
	t.Run("start stopped", func(t *testing.T) {
		service := &fakeManagedService{state: svc.Stopped}
		useFakeManager(t, &fakeServiceManager{service: service})
		if err := Start("llm-status-test"); err != nil || !service.started {
			t.Fatalf("Start() error = %v, service=%+v", err, service)
		}
	})
	t.Run("stop running and stopped", func(t *testing.T) {
		service := &fakeManagedService{state: svc.Running}
		useFakeManager(t, &fakeServiceManager{service: service})
		if err := Stop("llm-status-test"); err != nil || !service.controlled {
			t.Fatalf("Stop() error = %v, service=%+v", err, service)
		}
		service.controlled = false
		if err := Stop("llm-status-test"); err != nil || service.controlled {
			t.Fatalf("second Stop() error = %v, service=%+v", err, service)
		}
	})
	t.Run("status states", func(t *testing.T) {
		service := &fakeManagedService{state: svc.Running}
		manager := &fakeServiceManager{service: service}
		useFakeManager(t, manager)
		if state, err := Status("llm-status-test"); err != nil || state != StateRunning {
			t.Fatalf("running Status() = %v, %v", state, err)
		}
		service.state = svc.Stopped
		if state, err := Status("llm-status-test"); err != nil || state != StateStopped {
			t.Fatalf("stopped Status() = %v, %v", state, err)
		}
		manager.openErr = windows.ERROR_SERVICE_DOES_NOT_EXIST
		if state, err := Status("llm-status-test"); err != nil || state != StateNotInstalled {
			t.Fatalf("missing Status() = %v, %v", state, err)
		}
	})
}

func TestServiceHelpersReportFailures(t *testing.T) {
	service := &fakeManagedService{state: svc.Stopped, nonCrashErr: errors.New("flag failed")}
	if err := configureRecovery(service); err == nil || !strings.Contains(err.Error(), "non-crash") {
		t.Fatalf("configureRecovery() error = %v", err)
	}
	service = &fakeManagedService{queryErr: errors.New("query failed")}
	if err := doStart(service); err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("doStart() error = %v", err)
	}
	service = &fakeManagedService{state: svc.Stopped, startErr: errors.New("start failed")}
	if err := doStart(service); err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatalf("doStart() error = %v", err)
	}
	service = &fakeManagedService{state: svc.StartPending}
	if err := waitForState(service, svc.Running, time.Nanosecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("waitForState() error = %v", err)
	}
}
