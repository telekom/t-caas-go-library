// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command patch demonstrates pkg/patch against a local envtest API server
// (set KUBEBUILDER_ASSETS, for example via `make test-examples`):
//
//   - several workers increment a ConfigMap counter concurrently with
//     [patch.Object] without losing updates;
//   - a Node status condition is set with [patch.Status];
//   - a finalizer is added and removed with [patch.EnsureFinalizer] and
//     [patch.RemoveFinalizer].
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/telekom/t-caas-go-library/pkg/patch"
)

const (
	namespace   = "default"
	counterName = "counter"
	finalizer   = "example.com/cleanup"
	workers     = 4
)

func main() {
	lines, err := runEnvtest(ctrl.SetupSignalHandler())
	for _, l := range lines {
		fmt.Println(l)
	}
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

// runEnvtest starts a throw-away API server, runs the demo against it and
// returns the report lines.
func runEnvtest(ctx context.Context) (lines []string, err error) {
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	if err != nil {
		return nil, fmt.Errorf("start envtest (is KUBEBUILDER_ASSETS set?): %w", err)
	}
	defer func() {
		if stopErr := testEnv.Stop(); stopErr != nil {
			err = errors.Join(err, fmt.Errorf("stop envtest: %w", stopErr))
		}
	}()
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}
	r := &report{}
	err = run(ctx, c, r)
	return r.lines, err
}

// report collects human-readable result lines.
type report struct{ lines []string }

func (r *report) addf(format string, args ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

// run exercises every helper of pkg/patch and prints the observed results.
func run(ctx context.Context, c client.Client, out *report) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: counterName, Namespace: namespace},
		Data:       map[string]string{"count": "0"},
	}
	if err := c.Create(ctx, cm); err != nil {
		return fmt.Errorf("create ConfigMap: %w", err)
	}
	if _, err := patch.EnsureFinalizer(ctx, c, cm, finalizer); err != nil {
		return fmt.Errorf("ensure finalizer: %w", err)
	}
	out.addf("finalizers: %v", cm.Finalizers)

	if err := incrementConcurrently(ctx, c, client.ObjectKeyFromObject(cm)); err != nil {
		return fmt.Errorf("increment counter: %w", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(cm), cm); err != nil {
		return fmt.Errorf("get ConfigMap: %w", err)
	}
	out.addf("count: %s", cm.Data["count"])

	if err := setNodeCondition(ctx, c, out); err != nil {
		return fmt.Errorf("set Node condition: %w", err)
	}

	if err := c.Delete(ctx, cm); err != nil {
		return fmt.Errorf("delete ConfigMap: %w", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(cm), cm); err != nil {
		return fmt.Errorf("get ConfigMap: %w", err)
	}
	if _, err := patch.RemoveFinalizer(ctx, c, cm, finalizer); err != nil {
		return fmt.Errorf("remove finalizer: %w", err)
	}
	err := c.Get(ctx, client.ObjectKeyFromObject(cm), &corev1.ConfigMap{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("check ConfigMap deletion: %w", err)
	}
	out.addf("deleted: %t", apierrors.IsNotFound(err))
	return nil
}

// incrementConcurrently bumps the counter once per worker. Each read-modify-write
// would lose updates without the optimistic lock.
func incrementConcurrently(ctx context.Context, c client.Client, key client.ObjectKey) error {
	backoff := wait.Backoff{Steps: 50, Duration: 5 * time.Millisecond, Factor: 1.5, Jitter: 1, Cap: 100 * time.Millisecond}
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Go(func() {
			_, errs[i] = patch.Object(ctx, c, nil, backoff, key,
				func() *corev1.ConfigMap { return &corev1.ConfigMap{} },
				func(cm *corev1.ConfigMap) (bool, error) {
					n, err := strconv.Atoi(cm.Data["count"])
					if err != nil {
						return false, fmt.Errorf("parse count: %w", err)
					}
					cm.Data["count"] = strconv.Itoa(n + 1)
					return true, nil
				})
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// setNodeCondition records a custom condition on a Node via its status subresource.
func setNodeCondition(ctx context.Context, c client.Client, out *report) error {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}
	if err := c.Create(ctx, node); err != nil {
		return fmt.Errorf("create Node: %w", err)
	}
	node, err := patch.Status(ctx, c, nil, retry.DefaultRetry, client.ObjectKeyFromObject(node),
		func() *corev1.Node { return &corev1.Node{} },
		func(n *corev1.Node) (bool, error) {
			n.Status.Conditions = append(n.Status.Conditions,
				corev1.NodeCondition{Type: "Maintenance", Status: corev1.ConditionTrue, Reason: "Example"})
			return true, nil
		})
	if err != nil {
		return fmt.Errorf("patch Node status: %w", err)
	}
	out.addf("node condition: %s=%s", node.Status.Conditions[0].Type, node.Status.Conditions[0].Status)
	return nil
}
