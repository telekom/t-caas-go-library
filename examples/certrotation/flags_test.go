// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"slices"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestFlags(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "from-env")
	var o options
	flags := flag.NewFlagSet("certrotation", flag.ContinueOnError)
	bindFlags(flags, &o)
	if o.Namespace != "from-env" {
		t.Fatalf("namespace default = %q", o.Namespace)
	}
	if err := flags.Parse([]string{
		"--namespace=explicit", "--certs-dir=certs", "--disable-cert-rotation",
		"--cert-rotation-dns-name=hook.example", "--cert-rotation-service-name=hook",
		"--cert-rotation-secret-name=certs", "--port=9444", "--health-probe-bind-address=0",
		"--cert-rotation-validating-webhook=first,second", "--cert-rotation-validating-webhook=third",
		"--cert-rotation-mutating-webhook=mutating",
	}); err != nil {
		t.Fatal(err)
	}
	if o.Namespace != "explicit" || o.CertDir != "certs" || !o.Disabled ||
		o.DNSName != "hook.example" || o.ServiceName != "hook" || o.SecretName != "certs" ||
		o.port != 9444 || o.probeAddr != "0" ||
		!slices.Equal(o.ValidatingWebhooks, []string{"first", "second", "third"}) ||
		!slices.Equal(o.MutatingWebhooks, []string{"mutating"}) {
		t.Fatalf("parsed options: %+v", o)
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *rest.Config
		want string
	}{
		{name: "nil config", want: "creating manager"},
		{name: "missing certificate options", cfg: &rest.Config{Host: "https://127.0.0.1:1"}, want: "certificate rotation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run(t.Context(), tc.cfg, options{probeAddr: "0"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run error = %v, want %q", err, tc.want)
			}
		})
	}
}
