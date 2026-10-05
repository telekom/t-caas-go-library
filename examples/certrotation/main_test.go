// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/telekom/t-caas-go-library/pkg/certrotation"
)

const (
	namespace = "example"
	secret    = "example-webhook-certs"
	vwcName   = "example-validating"
)

func freeAddr(t *testing.T) (host string, port int) {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	a := l.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func TestRun(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via `make test-examples`")
	}
	ctrl.SetLogger(logr.Discard())

	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	t.Cleanup(func() { _ = testEnv.Stop() })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	none := admissionregistrationv1.SideEffectClassNone
	ignore := admissionregistrationv1.Ignore
	path := "/validate"
	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: vwcName},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: "validate.example.com",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				Service: &admissionregistrationv1.ServiceReference{Namespace: namespace, Name: "example-webhook", Path: &path},
			},
			SideEffects: &none, FailurePolicy: &ignore, AdmissionReviewVersions: []string{"v1"},
		}},
	}
	for _, obj := range []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: secret}},
		vwc,
	} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatalf("creating %T: %v", obj, err)
		}
	}

	certDir := t.TempDir()
	host, port := freeAddr(t)
	_, probePort := freeAddr(t)
	o := options{
		Config: certrotation.Config{
			Namespace:          namespace,
			SecretName:         secret,
			ServiceName:        "example-webhook",
			CertDir:            certDir,
			ValidatingWebhooks: []string{vwcName},
		},
		host:      host,
		port:      port,
		probeAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(probePort)),
	}
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, cfg, o) }()

	// Emulate the kubelet mounting the Secret into the cert dir.
	key := types.NamespacedName{Namespace: namespace, Name: secret}
	err = wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		var s corev1.Secret
		// Not found yet or not populated yet: keep polling.
		if populated := c.Get(ctx, key, &s) == nil && len(s.Data[corev1.TLSCertKey]) > 0; !populated {
			return false, nil
		}
		for _, k := range []string{corev1.TLSCertKey, corev1.TLSPrivateKeyKey} {
			if err := os.WriteFile(filepath.Join(certDir, k), s.Data[k], 0o600); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("waiting for certificate secret: %v", err)
	}

	readyz := "http://" + o.probeAddr + "/readyz"
	err = wait.PollUntilContextCancel(ctx, 250*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		select {
		case err := <-runErr:
			t.Fatalf("run exited early: %v", err)
		default:
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, readyz, http.NoBody)
		resp, _ := http.DefaultClient.Do(req)
		if resp == nil {
			return false, nil
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK, nil
	})
	if err != nil {
		t.Fatalf("waiting for readyz: %v", err)
	}

	var s corev1.Secret
	if err := c.Get(ctx, key, &s); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(vwc), vwc); err != nil {
		t.Fatal(err)
	}
	// Without RequireLeaderElection, readiness implies the CA has been injected.
	if !bytes.Equal(vwc.Webhooks[0].ClientConfig.CABundle, s.Data["ca.crt"]) {
		t.Fatal("caBundle not injected into ValidatingWebhookConfiguration")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(s.Data["ca.crt"]) {
		t.Fatal("generated CA is invalid")
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: time.Second},
		Config:    &tls.Config{RootCAs: roots, ServerName: "example-webhook.example.svc", MinVersion: tls.VersionTLS12},
	}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("readyz passed but webhook TLS endpoint is not reachable: %v", err)
	}
	_ = conn.Close()

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("run: %v", err)
	}
}
