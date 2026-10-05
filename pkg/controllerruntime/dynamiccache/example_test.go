// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache_test

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	"github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
)

// Wire the dynamic cache into a manager: only the NewCache option changes,
// controllers, field indexes and the manager's client work unchanged.
func ExampleNewCacheFunc() {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		fmt.Println(err)
		return
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		NewCache: dynamiccache.NewCacheFunc(dynamiccache.Options{
			NamespaceSelector: labels.SelectorFromSet(labels.Set{"example.com/tenant": "team-a"}),
		}),
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	_ = mgr // register controllers, then mgr.Start(ctx)
}

// Constructor arguments are validated eagerly: an empty selector would select
// every namespace, for which the stock cluster-wide cache is the right tool.
func ExampleNew() {
	_, err := dynamiccache.New(nil, cache.Options{}, dynamiccache.Options{})
	fmt.Println(err)

	// The rest.Config is never dialed: validation fails first.
	cfg := &rest.Config{Host: "https://127.0.0.1:1"}
	_, err = dynamiccache.NewCacheFunc(dynamiccache.Options{NamespaceSelector: labels.Everything()})(cfg, cache.Options{})
	fmt.Println(err)
	// Output:
	// dynamiccache: rest.Config must not be nil
	// dynamiccache: Options.NamespaceSelector must be set and non-empty
}

// Reads in a selected namespace whose cache is not running yet return the
// transient ErrNamespaceNotReady (never NotFound); requeue and retry.
func ExampleErrNamespaceNotReady() {
	var err error = &dynamiccache.ErrNamespaceNotReady{Namespace: "team-a"}

	var notReady *dynamiccache.ErrNamespaceNotReady
	if errors.As(err, &notReady) {
		fmt.Println("requeue: namespace", notReady.Namespace, "not ready")
	}
	// Output:
	// requeue: namespace team-a not ready
}
