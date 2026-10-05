// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package certrotation

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// newOfflineManager returns a manager that never needs to reach an API server
// as long as no informers or controllers are registered.
func newOfflineManager(t *testing.T) ctrl.Manager {
	t.Helper()
	mgr, err := ctrl.NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, ctrl.Options{
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func startManager(t *testing.T, mgr ctrl.Manager) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.Start(ctx) }()
	return cancel, errCh
}

func TestSetupWhenReadyArgs(t *testing.T) {
	t.Parallel()
	ok := func(context.Context) error { return nil }
	mgr := newOfflineManager(t)
	ready := make(chan struct{})
	for name, call := range map[string]func() error{
		"nil manager":     func() error { _, err := SetupWhenReady(nil, ready, ok); return err },
		"nil ready":       func() error { _, err := SetupWhenReady(mgr, nil, ok); return err },
		"no callbacks":    func() error { _, err := SetupWhenReady(mgr, ready); return err },
		"nil callback":    func() error { _, err := SetupWhenReady(mgr, ready, ok, nil); return err },
		"invalid rotator": func() error { _, err := AddRotator(t.Context(), mgr, Config{}); return err },
	} {
		if call() == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestSetupWhenReadyRunsCallbacksInOrder(t *testing.T) {
	t.Parallel()
	mgr := newOfflineManager(t)
	ready := make(chan struct{})
	var mu sync.Mutex
	var calls []string
	record := func(name string) func(context.Context) error {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, name)
			return nil
		}
	}
	done, err := SetupWhenReady(mgr, ready, record("a"), record("b"))
	if err != nil {
		t.Fatal(err)
	}
	check := ReadyChecker(done)
	cancel, errCh := startManager(t, mgr)
	defer cancel()

	time.Sleep(100 * time.Millisecond)
	if check(nil) == nil {
		t.Fatal("ready check passed before certificates were ready")
	}
	close(ready)
	waitClosed(t, done)
	if err := check(nil); err != nil {
		t.Fatalf("ready check after setup: %v", err)
	}
	mu.Lock()
	if !slices.Equal(calls, []string{"a", "b"}) {
		t.Fatalf("callbacks ran as %v, want [a b]", calls)
	}
	mu.Unlock()
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("manager: %v", err)
	}
}

func TestSetupWhenReadyCallbackError(t *testing.T) {
	t.Parallel()
	mgr := newOfflineManager(t)
	ready := make(chan struct{})
	close(ready)
	secondCalled := false
	done, err := SetupWhenReady(mgr, ready,
		func(context.Context) error { return errors.New("register failed") },
		func(context.Context) error { secondCalled = true; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	cancel, errCh := startManager(t, mgr)
	defer cancel()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "register failed") {
			t.Fatalf("manager error = %v, want callback error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("manager did not stop on callback error")
	}
	if secondCalled {
		t.Fatal("callback after failing one was called")
	}
	select {
	case <-done:
		t.Fatal("done closed despite callback error")
	default:
	}
}

func TestSetupWhenReadyCancelledBeforeReady(t *testing.T) {
	t.Parallel()
	mgr := newOfflineManager(t)
	done, err := SetupWhenReady(mgr, make(chan struct{}), func(context.Context) error {
		t.Error("callback must not run")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel, errCh := startManager(t, mgr)
	defer cancel()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("manager: %v", err)
	}
	select {
	case <-done:
		t.Fatal("done closed without ready")
	default:
	}
}
