// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command ssa-configmap applies a ConfigMap with the skip-if-unchanged
// ssa.Applier against the cluster selected by KUBECONFIG (or --kubeconfig).
//
// It applies the same desired state twice: the first call creates or patches
// the ConfigMap, the second is skipped without an apply request because the
// live object already matches and the field manager owns exactly the desired
// fields.
//
//	go run ./examples/ssa --namespace default --name ssa-demo --data mode=fast
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/t-caas-go-library/pkg/ssa"
)

var configMaps = ssa.Applier[*corev1.ConfigMap, *corev1ac.ConfigMapApplyConfiguration]{
	Kind:       "ConfigMap",
	Namespaced: true,
	New:        func() *corev1.ConfigMap { return &corev1.ConfigMap{} },
	Matches:    ssa.MatchesJSON[*corev1.ConfigMap, *corev1ac.ConfigMapApplyConfiguration],
	Extract:    corev1ac.ExtractConfigMap,
}

func main() {
	if err := run(ctrl.SetupSignalHandler(), nil, os.Stdout, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run parses args, connects to the cluster (cfg, or KUBECONFIG when nil) and
// applies the ConfigMap twice, printing each result to out.
func run(ctx context.Context, cfg *rest.Config, out io.Writer, args []string) error {
	flags := flag.NewFlagSet("ssa-configmap", flag.ContinueOnError)
	flags.SetOutput(out)
	namespace := flags.String("namespace", "default", "namespace of the ConfigMap")
	name := flags.String("name", "ssa-demo", "name of the ConfigMap")
	fieldOwner := flags.String("field-owner", "ssa-configmap-example", "SSA field manager")
	var data keyValues
	flags.Var(&data, "data", "key=value data entry (repeatable)")
	kubeconfig := flags.String("kubeconfig", "", "path to a kubeconfig (defaults to KUBECONFIG, then in-cluster)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if cfg == nil {
		var err error
		if *kubeconfig != "" {
			cfg, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		} else {
			cfg, err = ctrl.GetConfig()
		}
		if err != nil {
			return fmt.Errorf("load kubeconfig: %w", err)
		}
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("build scheme: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	for range 2 {
		// Build a fresh apply configuration for every call: Apply writes the
		// server response back into it.
		desired := corev1ac.ConfigMap(*name, *namespace).
			WithLabels(map[string]string{"app.kubernetes.io/managed-by": *fieldOwner}).
			WithData(data)
		result, err := configMaps.PatchApply(ctx, c, desired, false,
			client.FieldOwner(*fieldOwner), client.ForceOwnership)
		if err != nil {
			return fmt.Errorf("apply ConfigMap: %w", err)
		}
		if _, err := fmt.Fprintf(out, "ConfigMap %s/%s %s\n", *namespace, *name, result); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
	}
	return nil
}

// keyValues is a repeatable key=value flag.
type keyValues map[string]string

func (kv *keyValues) String() string {
	pairs := make([]string, 0, len(*kv))
	for key, value := range *kv {
		pairs = append(pairs, key+"="+value)
	}
	return strings.Join(pairs, ",")
}

func (kv *keyValues) Set(value string) error {
	key, val, ok := strings.Cut(value, "=")
	if !ok || key == "" {
		return errors.New("expected key=value")
	}
	if *kv == nil {
		*kv = keyValues{}
	}
	(*kv)[key] = val
	return nil
}
