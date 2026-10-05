// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package tracker_test

import (
	"context"
	"os"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/telekom/t-caas-go-library/pkg/discovery/tracker"
)

var (
	_ manager.Runnable               = (*tracker.Tracker)(nil)
	_ manager.LeaderElectionRunnable = (*tracker.Tracker)(nil)
)

func TestCRDCallbackEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("run make test to install and configure envtest")
	}
	environment := &envtest.Environment{}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	apiextensionsv1.AddToScheme(scheme)
	watcher, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	source, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	notifications := make(chan tracker.Snapshot, 100)
	instance, err := tracker.New(source, watcher, tracker.Options{
		Interval: time.Hour, Debounce: 20 * time.Millisecond,
		OnChange: func(_ context.Context, snapshot tracker.Snapshot) { notifications <- snapshot },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- instance.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	select {
	case <-notifications:
	case <-ctx.Done():
		t.Fatal("initial callback absent")
	}
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.org"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.org", Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets", Singular: "widget", Kind: "Widget"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"},
				},
			}},
		},
	}
	if err := watcher.Create(ctx, crd); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case snapshot := <-notifications:
			for _, resource := range snapshot["example.org/v1"] {
				if resource.Name == "widgets" {
					if len(resource.Verbs) == 0 {
						t.Fatal("discovery lost verbs")
					}
					for _, candidate := range snapshot["example.org/v1"] {
						if candidate.Name == "widgets/finalizers" {
							t.Fatal("synthetic auth-operator resource leaked")
						}
					}
					return
				}
			}
		case <-ctx.Done():
			t.Fatal("CRD watch did not trigger a discovery callback")
		}
	}
}
