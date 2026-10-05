// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package certrotation is a thin wrapper around the
// github.com/open-policy-agent/cert-controller rotator for self-managed
// admission webhook TLS certificates.
//
// [AddRotator] registers the rotator with a controller-runtime manager. The
// rotator generates a CA and serving certificate into a Secret, which the
// Deployment mounts at the webhook server's CertDir, and injects the CA into
// the configured Validating/MutatingWebhookConfigurations. The returned
// channel is closed once the serving certificate is usable:
//
//   - rotation enabled, RequireLeaderElection false: when the rotator reports
//     that the certificate is mounted and the CA is injected;
//   - rotation enabled, RequireLeaderElection true: additionally as soon as a
//     valid matching certificate and key appear in CertDir, so that non-leader replicas
//     (which never run the rotator) become ready too;
//   - rotation disabled: immediately (pre-provisioned certificates).
//
// Because the controller-runtime webhook server loads its certificate on start,
// webhook handlers should be registered only after the channel is closed, which
// [SetupWhenReady] does. [ReadyChecker] turns either channel into a readyz check.
//
// The rotator needs RBAC to get/list/watch/update Secrets in the Secret's
// namespace and get/list/watch/update
// the configured validatingwebhookconfigurations and
// mutatingwebhookconfigurations (admissionregistration.k8s.io). The Secret
// must exist before the rotator starts; create it in deployment manifests.
//
// controller-runtime enforces unique controller names per process. Set distinct
// Config.ControllerName values when registering multiple rotators.
package certrotation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	certFileName = "tls.crt"
	keyFileName  = "tls.key"
)

// pollInterval is how often CertDir is checked for a mounted certificate.
var pollInterval = time.Second

// AddRotator validates cfg, applies defaults and registers the cert-controller
// rotator (plus, with RequireLeaderElection, a watcher for the mounted
// certificate) with mgr. The returned channel is closed once the serving
// certificate is usable; see the package documentation for details.
//
// If cfg.Disabled is set, nothing is registered and the returned channel is
// already closed.
func AddRotator(ctx context.Context, mgr ctrl.Manager, cfg Config) (<-chan struct{}, error) {
	ready := make(chan struct{})
	if cfg.Disabled {
		close(ready)
		return ready, nil
	}
	if mgr == nil {
		return nil, fmt.Errorf("%w: manager is nil", ErrInvalidConfig)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	secretKey := types.NamespacedName{Namespace: cfg.Namespace, Name: cfg.SecretName}

	rotatorReady := ready
	if cfg.RequireLeaderElection {
		rotatorReady = make(chan struct{})
	}
	if err := rotator.AddRotator(mgr, &rotator.CertRotator{
		SecretKey:              secretKey,
		CertDir:                cfg.CertDir,
		CAName:                 cfg.CAName,
		CAOrganization:         cfg.CAOrganization,
		DNSName:                cfg.DNSName,
		ExtraDNSNames:          cfg.ExtraDNSNames,
		IsReady:                rotatorReady,
		Webhooks:               cfg.webhooks(),
		FieldOwner:             cfg.FieldOwner,
		RestartOnSecretRefresh: cfg.RestartOnSecretRefresh,
		RequireLeaderElection:  cfg.RequireLeaderElection,
		ControllerName:         cfg.ControllerName,
	}); err != nil {
		return nil, fmt.Errorf("adding cert rotator: %w", err)
	}
	if !cfg.RequireLeaderElection {
		return ready, nil
	}

	w := &mountWatcher{certDir: cfg.CertDir, rotatorReady: rotatorReady, ready: ready}
	if err := mgr.Add(nonLeaderRunnable(w.start)); err != nil {
		return nil, fmt.Errorf("adding certificate mount watcher: %w", err)
	}
	return ready, nil
}

// ReadyChecker returns a healthz.Checker that fails until ready is closed.
func ReadyChecker(ready <-chan struct{}) healthz.Checker {
	return func(_ *http.Request) error {
		select {
		case <-ready:
			return nil
		default:
			return errors.New("webhook certificates not ready")
		}
	}
}

// SetupWhenReady registers a runnable (on every replica, independent of leader
// election) that waits for ready and then calls the setup callbacks in order,
// typically registering webhook handlers via mgr.GetWebhookServer() or
// ctrl.NewWebhookManagedBy. The returned channel is closed after all callbacks
// succeeded; pass it to [ReadyChecker] for a readyz check. The first callback
// error stops the manager. Cancellation before ready is not an error.
func SetupWhenReady(mgr ctrl.Manager, ready <-chan struct{}, setups ...func(ctx context.Context) error) (<-chan struct{}, error) {
	hasNil := slices.ContainsFunc(setups, func(f func(context.Context) error) bool { return f == nil })
	if mgr == nil || ready == nil || len(setups) == 0 || hasNil {
		return nil, errors.New("certrotation: manager, ready channel and setup callbacks must not be nil")
	}
	done := make(chan struct{})
	err := mgr.Add(nonLeaderRunnable(func(ctx context.Context) error {
		logger := log.FromContext(ctx).WithName("certrotation")
		logger.Info("waiting for webhook certificates")
		select {
		case <-ctx.Done():
			return nil
		case <-ready:
		}
		for i, setup := range setups {
			if err := setup(ctx); err != nil {
				return fmt.Errorf("webhook setup callback %d after certificate rotation: %w", i, err)
			}
		}
		logger.Info("webhook certificates ready, setup complete")
		close(done)
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("adding webhook setup runnable: %w", err)
	}
	return done, nil
}

// nonLeaderRunnable is a manager runnable that runs on every replica.
type nonLeaderRunnable func(ctx context.Context) error

// Start implements manager.Runnable.
func (r nonLeaderRunnable) Start(ctx context.Context) error { return r(ctx) }

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (nonLeaderRunnable) NeedLeaderElection() bool { return false }

// mountWatcher closes ready when either the rotator reports readiness (leader)
// or a valid certificate and matching key are present in certDir (any replica).
type mountWatcher struct {
	certDir      string
	rotatorReady <-chan struct{}
	ready        chan struct{}
}

func (w *mountWatcher) start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("certrotation")
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if certsMounted(w.certDir) {
			logger.Info("webhook certificate found in cert dir", "certDir", w.certDir)
			close(w.ready)
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-w.rotatorReady:
			logger.Info("cert rotator reported readiness")
			close(w.ready)
			return nil
		case <-ticker.C:
		}
	}
}

// certsMounted reports whether certDir contains a matching TLS certificate and
// key with a currently valid leaf certificate.
func certsMounted(certDir string) bool {
	cert, err := tls.LoadX509KeyPair(filepath.Join(certDir, certFileName), filepath.Join(certDir, keyFileName))
	if err != nil {
		return false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return false
	}
	now := time.Now()
	return !now.Before(leaf.NotBefore) && now.Before(leaf.NotAfter)
}
