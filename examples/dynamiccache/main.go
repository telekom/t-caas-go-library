// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command dynamiccache runs a controller-runtime manager whose cache only
// watches namespaces matching a label selector, using
// dynamiccache.NewCacheFunc. A ConfigMap reconciler prints every object it
// reconciles; ConfigMaps in namespaces that do not match the selector are
// never seen, and namespaces that gain the label later are picked up without
// a restart.
//
//	go run ./examples/dynamiccache --selector example.com/watched=true
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
)

var skipNameValidation = true

func main() {
	if err := run(ctrl.SetupSignalHandler(), nil, os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run parses args, connects to the cluster (cfg, or KUBECONFIG when nil) and
// runs the manager until ctx is cancelled, printing every reconciled
// ConfigMap key to out.
func run(ctx context.Context, cfg *rest.Config, out io.Writer, args []string) error {
	flags := flag.NewFlagSet("dynamiccache", flag.ContinueOnError)
	flags.SetOutput(out)
	selector := flags.String("selector", "example.com/watched=true", "label selector choosing the watched namespaces")
	kubeconfig := flags.String("kubeconfig", "", "path to a kubeconfig (defaults to KUBECONFIG / in-cluster)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	nsSelector, err := labels.Parse(*selector)
	if err != nil {
		return fmt.Errorf("parsing --selector: %w", err)
	}
	if nsSelector.Empty() {
		return errors.New("--selector must not be empty")
	}

	if cfg == nil {
		if *kubeconfig != "" {
			cfg, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		} else {
			cfg, err = ctrl.GetConfig()
		}
		if err != nil {
			return fmt.Errorf("loading kubeconfig: %w", err)
		}
	}

	// The only change compared to a stock manager: NewCache.
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		NewCache: dynamiccache.NewCacheFunc(dynamiccache.Options{
			NamespaceSelector: nsSelector,
		}),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// Allows run to be invoked repeatedly in one process (tests).
		Controller: config.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	var mu sync.Mutex
	err = ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Complete(reconcile.Func(func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
			mu.Lock()
			defer mu.Unlock()
			_, err := fmt.Fprintf(out, "reconciled ConfigMap %s\n", req.NamespacedName)
			return reconcile.Result{}, err
		}))
	if err != nil {
		return fmt.Errorf("creating controller: %w", err)
	}

	return mgr.Start(ctx)
}
