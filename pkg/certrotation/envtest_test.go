// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package certrotation_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"slices"
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
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/telekom/t-caas-go-library/pkg/certrotation"
)

const (
	testNamespace = "certrotation-test"
	testService   = "webhook-svc"
	testSecret    = "webhook-certs"
	testVWC       = "test-validating"
	testMWC       = "test-mutating"
)

// mountSecret emulates the kubelet projecting the Secret into dir.
func mountSecret(ctx context.Context, c client.Client, key types.NamespacedName, dir string) {
	_ = wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		var s corev1.Secret
		// Not found yet or not populated yet: keep polling.
		if populated := c.Get(ctx, key, &s) == nil && len(s.Data[corev1.TLSCertKey]) > 0; !populated {
			return false, nil
		}
		for _, k := range []string{corev1.TLSCertKey, corev1.TLSPrivateKeyKey} {
			if err := os.WriteFile(filepath.Join(dir, k), s.Data[k], 0o600); err != nil {
				return false, err
			}
		}
		return true, nil
	})
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func webhookFixtures() (*admissionregistrationv1.ValidatingWebhookConfiguration, *admissionregistrationv1.MutatingWebhookConfiguration) {
	none := admissionregistrationv1.SideEffectClassNone
	ignore := admissionregistrationv1.Ignore
	path := "/validate"
	cc := admissionregistrationv1.WebhookClientConfig{
		Service: &admissionregistrationv1.ServiceReference{Namespace: testNamespace, Name: testService, Path: &path},
	}
	rules := []admissionregistrationv1.RuleWithOperations{{
		Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
		Rule: admissionregistrationv1.Rule{
			APIGroups: []string{"example.com"}, APIVersions: []string{"v1"}, Resources: []string{"widgets"},
		},
	}}
	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: testVWC},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: "validate.example.com", ClientConfig: cc, Rules: rules,
			SideEffects: &none, FailurePolicy: &ignore, AdmissionReviewVersions: []string{"v1"},
		}},
	}
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: testMWC},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: "mutate.example.com", ClientConfig: cc, Rules: rules,
			SideEffects: &none, FailurePolicy: &ignore, AdmissionReviewVersions: []string{"v1"},
		}},
	}
	return vwc, mwc
}

func TestAddRotatorEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via `make test`")
	}
	t.Run("leader-only rotation", func(t *testing.T) { testAddRotator(t, true) })
	t.Run("all-replica rotation", func(t *testing.T) { testAddRotator(t, false) })
}

func testAddRotator(t *testing.T, requireLeaderElection bool) {
	t.Helper()
	log.SetLogger(logr.Discard())

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
	vwc, mwc := webhookFixtures()
	objs := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testSecret},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{corev1.TLSCertKey: {}, corev1.TLSPrivateKeyKey: {}},
		},
		vwc, mwc,
	}
	for _, obj := range objs {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatalf("creating %T: %v", obj, err)
		}
	}

	certDir := t.TempDir()
	port := freePort(t)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Metrics:                       metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:        "0",
		LeaderElection:                true,
		LeaderElectionID:              "certrotation-test",
		LeaderElectionNamespace:       testNamespace,
		LeaderElectionReleaseOnCancel: true,
		WebhookServer:                 webhook.NewServer(webhook.Options{Host: "127.0.0.1", Port: port, CertDir: certDir}),
	})
	if err != nil {
		t.Fatal(err)
	}

	ready, err := certrotation.AddRotator(ctx, mgr, certrotation.Config{
		Namespace:          testNamespace,
		SecretName:         testSecret,
		ControllerName:     t.Name(),
		ServiceName:        testService,
		CertDir:            certDir,
		CAOrganization:     "example",
		ValidatingWebhooks: []string{testVWC},
		MutatingWebhooks:   []string{testMWC},
		// Keep restart disabled: enabling it exits after writing the Secret.
		RequireLeaderElection: requireLeaderElection,
	})
	if err != nil {
		t.Fatalf("AddRotator: %v", err)
	}
	setupDone, err := certrotation.SetupWhenReady(mgr, ready, func(context.Context) error {
		mgr.GetWebhookServer().Register("/validate", &admission.Webhook{
			Handler: admission.HandlerFunc(func(context.Context, admission.Request) admission.Response {
				return admission.Allowed("")
			}),
		})
		return nil
	})
	if err != nil {
		t.Fatalf("SetupWhenReady: %v", err)
	}
	checker := certrotation.ReadyChecker(setupDone)
	if checker(nil) == nil {
		t.Fatal("ready check passed before certificates were provisioned")
	}

	mgrErr := make(chan error, 1)
	go func() { mgrErr <- mgr.Start(ctx) }()
	go mountSecret(ctx, c, types.NamespacedName{Namespace: testNamespace, Name: testSecret}, certDir)

	select {
	case <-ready:
	case err := <-mgrErr:
		t.Fatalf("manager exited before ready: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for certificate readiness")
	}

	caPEM := assertSecretAndCABundles(ctx, t, c, vwc, mwc)

	select {
	case <-setupDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for webhook setup")
	}
	if err := checker(nil); err != nil {
		t.Fatalf("ready check failed after setup: %v", err)
	}

	assertServingCert(ctx, t, caPEM, port)

	cancel()
	if err := <-mgrErr; err != nil {
		t.Fatalf("manager: %v", err)
	}
}

func assertSecretAndCABundles(
	ctx context.Context, t *testing.T, c client.Client,
	vwc *admissionregistrationv1.ValidatingWebhookConfiguration, mwc *admissionregistrationv1.MutatingWebhookConfiguration,
) []byte {
	t.Helper()
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: testSecret}, &secret); err != nil {
		t.Fatal(err)
	}
	if secret.Type != corev1.SecretTypeTLS {
		t.Fatalf("secret type = %q, want %q", secret.Type, corev1.SecretTypeTLS)
	}
	caPEM := secret.Data["ca.crt"]
	for _, k := range []string{"ca.crt", "ca.key", corev1.TLSCertKey, corev1.TLSPrivateKeyKey} {
		if len(secret.Data[k]) == 0 {
			t.Fatalf("secret key %q not populated", k)
		}
	}

	// With RequireLeaderElection, readiness may be signalled by the mounted
	// files before the leader has injected the CA, so poll for the injection.
	err := wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKeyFromObject(vwc), vwc); err != nil {
			return false, err
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(mwc), mwc); err != nil {
			return false, err
		}
		return bytes.Equal(vwc.Webhooks[0].ClientConfig.CABundle, caPEM) &&
			bytes.Equal(mwc.Webhooks[0].ClientConfig.CABundle, caPEM), nil
	})
	if err != nil {
		t.Fatalf("caBundle not injected into webhook configurations: %v", err)
	}
	return caPEM
}

// assertServingCert checks that the webhook server serves the rotated
// certificate, valid for the service DNS names.
func assertServingCert(ctx context.Context, t *testing.T, caPEM []byte, port int) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	serverName := testService + "." + testNamespace + ".svc"
	var conn *tls.Conn
	err := wait.PollUntilContextCancel(ctx, 200*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		d := tls.Dialer{Config: &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12}}
		nc, _ := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		conn, _ = nc.(*tls.Conn)
		return conn != nil, nil
	})
	if err != nil {
		t.Fatalf("TLS handshake with webhook server: %v", err)
	}
	leaf := conn.ConnectionState().PeerCertificates[0]
	_ = conn.Close()
	for _, n := range []string{serverName, serverName + ".cluster.local", testService} {
		if !slices.Contains(leaf.DNSNames, n) {
			t.Fatalf("serving certificate SANs %v miss %q", leaf.DNSNames, n)
		}
	}
}
