// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command tracker demonstrates discovery snapshots using an offline source.
// For a cluster use discovery.NewDiscoveryClientForConfig and client.NewWithWatch, then add the
// tracker to a manager or run Start(ctx) for polling and CRD watch updates.
package main

import (
	"context"
	"fmt"
	"log"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telekom/t-caas-go-library/pkg/discovery/tracker"
)

type source struct{}

func (source) ServerGroupsAndResourcesWithContext(context.Context) ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, []*metav1.APIResourceList{{GroupVersion: "v1",
		APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: []string{"get", "list"}}},
	}}, nil
}

func run(ctx context.Context, discoverySource tracker.Source) (tracker.Snapshot, error) {
	instance, err := tracker.New(discoverySource, nil, tracker.Options{})
	if err != nil {
		return nil, err
	}
	if _, err := instance.Refresh(ctx); err != nil {
		return nil, err
	}
	return instance.Snapshot()
}

func main() {
	snapshot, err := run(context.Background(), source{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(snapshot["v1"][0].Name)
}
