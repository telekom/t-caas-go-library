// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package certrotation_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/telekom/t-caas-go-library/pkg/certrotation"
)

func ExampleConfig_Validate() {
	err := certrotation.Config{Namespace: "my-system", DNSName: "my-webhook.my-system.svc"}.Validate()
	fmt.Println(errors.Is(err, certrotation.ErrInvalidConfig))
	fmt.Println(certrotation.Config{Disabled: true}.Validate())
	// Output:
	// true
	// <nil>
}

// ExampleAddRotator shows the typical wiring in a webhook binary: rotate
// certificates, register handlers once they are usable and gate readyz.
func ExampleAddRotator() {
	ctx := ctrl.SetupSignalHandler()
	server := webhook.NewServer(webhook.Options{})
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		LeaderElection:          true,
		LeaderElectionID:        "my-webhook-certrotation",
		LeaderElectionNamespace: "my-system",
		WebhookServer:           server,
	})
	if err != nil {
		panic(err)
	}
	ready, err := certrotation.AddRotator(ctx, mgr, certrotation.Config{
		Namespace:             "my-system",
		SecretName:            "my-webhook-certs",
		ServiceName:           "my-webhook",
		ValidatingWebhooks:    []string{"my-validating-webhook-configuration"},
		RequireLeaderElection: true,
	})
	if err != nil {
		panic(err)
	}
	// Callbacks run in order on every replica once certificates are usable,
	// e.g. ctrl.NewWebhookManagedBy(mgr, &MyType{}).Complete().
	setupFooWebhook := func(context.Context) error {
		mgr.GetWebhookServer().Register("/foo", http.NotFoundHandler())
		return nil
	}
	setupBarWebhook := func(context.Context) error {
		mgr.GetWebhookServer().Register("/bar", http.NotFoundHandler())
		return nil
	}
	setupDone, err := certrotation.SetupWhenReady(mgr, ready, setupFooWebhook, setupBarWebhook)
	if err != nil {
		panic(err)
	}
	if err := mgr.AddReadyzCheck("webhook-certs", certrotation.ReadyChecker(setupDone)); err != nil {
		panic(err)
	}
	if err := mgr.AddReadyzCheck("webhook-server", server.StartedChecker()); err != nil {
		panic(err)
	}
	if err := mgr.Start(ctx); err != nil {
		panic(err)
	}
}
