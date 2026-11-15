// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package certrotation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	valid := Config{Namespace: "ns", SecretName: "certs", DNSName: "svc.ns.svc"}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "valid with DNS name", mutate: func(*Config) {}},
		{name: "valid with service name", mutate: func(c *Config) { c.DNSName, c.ServiceName = "", "svc" }},
		{name: "disabled zero value", mutate: func(c *Config) { *c = Config{Disabled: true} }},
		{name: "missing namespace", mutate: func(c *Config) { c.Namespace = "" }, wantErr: "namespace is required"},
		{name: "missing secret", mutate: func(c *Config) { c.SecretName = "" }, wantErr: "secret name is required"},
		{name: "missing DNS and service", mutate: func(c *Config) { c.DNSName = "" }, wantErr: "DNS name or service name"},
		{
			name:    "empty validating webhook",
			mutate:  func(c *Config) { c.ValidatingWebhooks = []string{"a", ""} },
			wantErr: "validating webhook names",
		},
		{
			name:    "empty mutating webhook",
			mutate:  func(c *Config) { c.MutatingWebhooks = []string{""} },
			wantErr: "mutating webhook names",
		},
		{name: "zero value", mutate: func(c *Config) { *c = Config{} }, wantErr: "namespace is required"},
		{name: "custom lifetimes", mutate: func(c *Config) {
			c.CAValidity, c.CertValidity, c.RenewBefore, c.CheckInterval = 365*24*time.Hour, 30*24*time.Hour, 7*24*time.Hour, time.Hour
		}},
		{
			name: "sub-second lifetimes",
			mutate: func(c *Config) {
				c.CAValidity, c.CertValidity = time.Millisecond, time.Millisecond
				c.RenewBefore, c.CheckInterval = 900*time.Microsecond, 100*time.Microsecond
			},
			wantErr: "certificate validity must be zero (default) or at least 1s, got 1ms",
		},
		{name: "certificate validity equals CA validity", mutate: func(c *Config) { c.CAValidity = DefaultCertValidity }},
		{
			name:    "negative CA validity",
			mutate:  func(c *Config) { c.CAValidity = -time.Hour },
			wantErr: "CA validity must be zero (default) or at least 1s",
		},
		{
			name:    "negative check interval",
			mutate:  func(c *Config) { c.CheckInterval = -time.Hour },
			wantErr: "check interval must be zero (default) or at least 1s",
		},
		{
			name:    "certificate validity above CA validity",
			mutate:  func(c *Config) { c.CAValidity = 180 * 24 * time.Hour },
			wantErr: "must not exceed CA validity",
		},
		{
			name:    "certificate validity below default renew before",
			mutate:  func(c *Config) { c.CertValidity = 30 * 24 * time.Hour },
			wantErr: "renew before 2160h0m0s must be less than certificate validity 720h0m0s",
		},
		{
			name:    "renew before equals certificate validity",
			mutate:  func(c *Config) { c.RenewBefore = DefaultCertValidity },
			wantErr: "must be less than certificate validity",
		},
		{
			name:    "renew before not above check interval",
			mutate:  func(c *Config) { c.RenewBefore = 12 * time.Hour },
			wantErr: "check interval 12h0m0s must be less than renew before",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := valid
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error %v does not wrap ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestWithDefaults(t *testing.T) {
	t.Parallel()
	defaultDir := filepath.Join(os.TempDir(), "k8s-webhook-server", "serving-certs")
	tests := []struct {
		name string
		in   Config
		want Config
	}{
		{
			name: "explicit DNS name (auth-operator style)",
			in:   Config{Namespace: "ns", SecretName: "certs", DNSName: "hook.ns.svc", CertDir: "/certs", CAName: "cert"},
			want: Config{Namespace: "ns", SecretName: "certs", DNSName: "hook.ns.svc", CertDir: "/certs", CAName: "cert"},
		},
		{
			name: "service name derives names (k8s-breakglass style)",
			in:   Config{Namespace: "ns", SecretName: "hook", ServiceName: "hook"},
			want: Config{
				Namespace: "ns", SecretName: "hook", ServiceName: "hook",
				DNSName:       "hook.ns.svc",
				ExtraDNSNames: []string{"hook.ns.svc.cluster.local", "hook"},
				CertDir:       defaultDir,
				CAName:        "hook-ca",
			},
		},
		{
			name: "service name with explicit DNS name and dedup",
			in: Config{
				Namespace: "ns", SecretName: "certs", ServiceName: "hook", DNSName: "hook.example.com",
				ExtraDNSNames: []string{"hook", "", "hook.example.com", "other"},
			},
			want: Config{
				Namespace: "ns", SecretName: "certs", ServiceName: "hook", DNSName: "hook.example.com",
				ExtraDNSNames: []string{"hook", "other", "hook.ns.svc", "hook.ns.svc.cluster.local"},
				CertDir:       defaultDir,
				CAName:        "certs-ca",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.in.withDefaults()
			tt.want.CAValidity, tt.want.CertValidity = DefaultCAValidity, DefaultCertValidity
			tt.want.RenewBefore, tt.want.CheckInterval = DefaultRenewBefore, DefaultCheckInterval
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("withDefaults() =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestCertRotator(t *testing.T) {
	t.Parallel()
	ready := make(chan struct{})
	base := Config{
		Namespace: "ns", SecretName: "certs", ControllerName: "rot", DNSName: "hook.ns.svc", ExtraDNSNames: []string{"hook"},
		CertDir: "/certs", CAName: "ca", CAOrganization: "org", ValidatingWebhooks: []string{"v"}, FieldOwner: "fo",
		RestartOnSecretRefresh: true, RequireLeaderElection: true,
	}
	want := rotator.CertRotator{
		SecretKey: types.NamespacedName{Namespace: "ns", Name: "certs"}, CertDir: "/certs", CAName: "ca", CAOrganization: "org",
		DNSName: "hook.ns.svc", ExtraDNSNames: []string{"hook"}, IsReady: ready,
		Webhooks: []rotator.WebhookInfo{{Name: "v", Type: rotator.Validating}}, FieldOwner: "fo",
		RestartOnSecretRefresh: true, RequireLeaderElection: true, ControllerName: "rot",
		// cert-controller's defaults: unchanged when the fields are zero.
		CaCertDuration: 10 * 365 * 24 * time.Hour, ServerCertDuration: 365 * 24 * time.Hour,
		LookaheadInterval: 90 * 24 * time.Hour, RotationCheckFrequency: 12 * time.Hour,
	}
	if got := base.withDefaults().certRotator(ready); !reflect.DeepEqual(*got, want) {
		t.Fatalf("defaults: certRotator() =\n%+v\nwant\n%+v", *got, want)
	}

	custom := base
	custom.CAValidity, custom.CertValidity = 365*24*time.Hour, 30*24*time.Hour
	custom.RenewBefore, custom.CheckInterval = 7*24*time.Hour, time.Hour
	want.CaCertDuration, want.ServerCertDuration = custom.CAValidity, custom.CertValidity
	want.LookaheadInterval, want.RotationCheckFrequency = custom.RenewBefore, custom.CheckInterval
	if got := custom.withDefaults().certRotator(ready); !reflect.DeepEqual(*got, want) {
		t.Fatalf("custom: certRotator() =\n%+v\nwant\n%+v", *got, want)
	}
}

func TestWithDefaultsDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	extra := []string{"a"}
	in := Config{Namespace: "ns", SecretName: "s", ServiceName: "svc", ExtraDNSNames: extra}
	_ = in.withDefaults()
	if len(extra) != 1 || extra[0] != "a" {
		t.Fatalf("input slice mutated: %v", extra)
	}
}

func TestWebhooks(t *testing.T) {
	t.Parallel()
	cfg := Config{ValidatingWebhooks: []string{"v1", "v2"}, MutatingWebhooks: []string{"m1"}}
	want := []rotator.WebhookInfo{
		{Name: "m1", Type: rotator.Mutating},
		{Name: "v1", Type: rotator.Validating},
		{Name: "v2", Type: rotator.Validating},
	}
	if got := cfg.webhooks(); !reflect.DeepEqual(got, want) {
		t.Fatalf("webhooks() = %+v, want %+v", got, want)
	}
	if got := (Config{}).webhooks(); len(got) != 0 {
		t.Fatalf("webhooks() of empty config = %+v, want empty", got)
	}
}

func TestAddRotatorDisabled(t *testing.T) {
	t.Parallel()
	ready, err := AddRotator(t.Context(), nil, Config{Disabled: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case <-ready:
	default:
		t.Fatal("ready channel not closed for disabled config")
	}
}

func TestAddRotatorErrors(t *testing.T) {
	t.Parallel()
	if _, err := AddRotator(t.Context(), nil, Config{Namespace: "ns", SecretName: "s", DNSName: "d"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil manager: got %v, want ErrInvalidConfig", err)
	}
}

func TestReadyChecker(t *testing.T) {
	t.Parallel()
	ready := make(chan struct{})
	check := ReadyChecker(ready)
	if err := check(nil); err == nil {
		t.Fatal("expected error before ready")
	}
	close(ready)
	if err := check(nil); err != nil {
		t.Fatalf("unexpected error after ready: %v", err)
	}
}

const testPEM = "-----BEGIN TEST-----\nAAAA\n-----END TEST-----\n"

func certificatePair(t *testing.T, notBefore, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: notBefore, NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCertsMounted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if certsMounted(dir) {
		t.Fatal("empty dir reported as mounted")
	}
	writeFile(t, dir, certFileName, testPEM)
	if certsMounted(dir) {
		t.Fatal("dir without key reported as mounted")
	}
	writeFile(t, dir, keyFileName, "not pem")
	if certsMounted(dir) {
		t.Fatal("non-PEM key reported as mounted")
	}
	writeFile(t, dir, keyFileName, testPEM)
	if certsMounted(dir) {
		t.Fatal("arbitrary PEM reported as mounted")
	}
	now := time.Now()
	cert, key := certificatePair(t, now.Add(-time.Hour), now.Add(time.Hour))
	writeFile(t, dir, certFileName, cert)
	writeFile(t, dir, keyFileName, key)
	if !certsMounted(dir) {
		t.Fatal("valid cert and key not reported as mounted")
	}
	_, otherKey := certificatePair(t, now.Add(-time.Hour), now.Add(time.Hour))
	writeFile(t, dir, keyFileName, otherKey)
	if certsMounted(dir) {
		t.Fatal("mismatched cert and key reported as mounted")
	}
	for _, validity := range [][2]time.Time{
		{now.Add(-2 * time.Hour), now.Add(-time.Hour)},
		{now.Add(time.Hour), now.Add(2 * time.Hour)},
	} {
		cert, key := certificatePair(t, validity[0], validity[1])
		writeFile(t, dir, certFileName, cert)
		writeFile(t, dir, keyFileName, key)
		if certsMounted(dir) {
			t.Fatal("certificate outside validity period reported as mounted")
		}
	}
}

func runWatcher(t *testing.T, w *mountWatcher) (cancel context.CancelFunc, errCh <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ch := make(chan error, 1)
	go func() { ch <- nonLeaderRunnable(w.start).Start(ctx) }()
	return cancel, ch
}

func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("channel not closed in time")
	}
}

func TestMountWatcher(t *testing.T) {
	t.Run("files appear (non-leader)", func(t *testing.T) {
		dir := t.TempDir()
		w := &mountWatcher{certDir: dir, rotatorReady: make(chan struct{}), ready: make(chan struct{})}
		cancel, errCh := runWatcher(t, w)
		defer cancel()
		cert, key := certificatePair(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		writeFile(t, dir, certFileName, cert)
		writeFile(t, dir, keyFileName, key)
		waitClosed(t, w.ready)
		if err := <-errCh; err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("rotator ready (leader)", func(t *testing.T) {
		rotatorReady := make(chan struct{})
		w := &mountWatcher{certDir: t.TempDir(), rotatorReady: rotatorReady, ready: make(chan struct{})}
		cancel, errCh := runWatcher(t, w)
		defer cancel()
		close(rotatorReady)
		waitClosed(t, w.ready)
		if err := <-errCh; err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("context cancelled", func(t *testing.T) {
		w := &mountWatcher{certDir: t.TempDir(), rotatorReady: make(chan struct{}), ready: make(chan struct{})}
		cancel, errCh := runWatcher(t, w)
		cancel()
		if err := <-errCh; err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		select {
		case <-w.ready:
			t.Fatal("ready closed after cancellation")
		default:
		}
	})
}

func TestNonLeaderRunnable(t *testing.T) {
	t.Parallel()
	if nonLeaderRunnable(nil).NeedLeaderElection() {
		t.Fatal("NeedLeaderElection() = true, want false")
	}
}
