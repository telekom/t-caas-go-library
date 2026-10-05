// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command certrotation is a minimal admission webhook server whose TLS
// certificate is generated and rotated by pkg/certrotation.
//
// Deploy it with a Service, an (initially empty) Secret mounted at --certs-dir
// and a ValidatingWebhookConfiguration pointing at the Service path /validate:
//
//	certrotation --namespace=my-system --cert-rotation-secret-name=my-webhook-certs \
//	  --cert-rotation-service-name=my-webhook \
//	  --cert-rotation-validating-webhook=my-validating-webhook-configuration
//
// With --disable-cert-rotation it serves pre-provisioned certificates instead.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/telekom/t-caas-go-library/pkg/certrotation"
)

type options struct {
	certrotation.Config
	host      string
	port      int
	probeAddr string
}

func main() {
	var o options
	bindFlags(flag.CommandLine, &o)
	flag.Parse()

	ctrl.SetLogger(zap.New())
	if err := run(ctrl.SetupSignalHandler(), ctrl.GetConfigOrDie(), o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func bindFlags(flags *flag.FlagSet, o *options) {
	list := func(dst *[]string) func(string) error {
		return func(s string) error { *dst = append(*dst, strings.Split(s, ",")...); return nil }
	}
	flags.StringVar(&o.Namespace, "namespace", os.Getenv("POD_NAMESPACE"), "Namespace of the certificate Secret")
	flags.StringVar(&o.CertDir, "certs-dir", "", "Directory the certificate Secret is mounted at")
	flags.BoolVar(&o.Disabled, "disable-cert-rotation", false, "Use pre-provisioned certificates from --certs-dir")
	flags.StringVar(&o.DNSName, "cert-rotation-dns-name", "", "Primary DNS name of the serving certificate")
	flags.StringVar(&o.ServiceName, "cert-rotation-service-name", "", "Webhook Service name (derives in-cluster DNS names)")
	flags.StringVar(&o.SecretName, "cert-rotation-secret-name", "", "Name of the certificate Secret")
	flags.Func("cert-rotation-validating-webhook", "ValidatingWebhookConfiguration to inject the CA into (repeatable)",
		list(&o.ValidatingWebhooks))
	flags.Func("cert-rotation-mutating-webhook", "MutatingWebhookConfiguration to inject the CA into (repeatable)",
		list(&o.MutatingWebhooks))
	flags.IntVar(&o.port, "port", webhook.DefaultPort, "Webhook server port")
	flags.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "Health probe address")
}

func run(ctx context.Context, restCfg *rest.Config, o options) error {
	server := webhook.NewServer(webhook.Options{Host: o.host, Port: o.port, CertDir: o.CertDir})
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: o.probeAddr,
		WebhookServer:          server,
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	ready, err := certrotation.AddRotator(ctx, mgr, o.Config)
	if err != nil {
		return fmt.Errorf("setting up certificate rotation: %w", err)
	}
	// The webhook server loads its certificate on start, so register handlers
	// (which adds the server to the manager) only once certificates are usable.
	setupDone, err := certrotation.SetupWhenReady(mgr, ready, func(context.Context) error {
		mgr.GetWebhookServer().Register("/validate", &admission.Webhook{
			Handler: admission.HandlerFunc(func(context.Context, admission.Request) admission.Response {
				return admission.Allowed("")
			}),
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("setting up webhooks: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("adding healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("webhook-certs", certrotation.ReadyChecker(setupDone)); err != nil {
		return fmt.Errorf("adding readyz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("webhook-server", server.StartedChecker()); err != nil {
		return fmt.Errorf("adding webhook server readyz check: %w", err)
	}
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("running manager: %w", err)
	}
	return nil
}
