// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package certrotation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/open-policy-agent/cert-controller/pkg/rotator"
)

// ErrInvalidConfig is returned (wrapped) by [Config.Validate] and [AddRotator]
// when the configuration is incomplete or inconsistent.
var ErrInvalidConfig = errors.New("certrotation: invalid config")

// Config configures webhook certificate rotation.
//
// The zero value is not usable unless Disabled is set. Required fields when
// rotation is enabled: Namespace, SecretName and either DNSName or ServiceName.
type Config struct {
	// Disabled turns rotation off. [AddRotator] then registers nothing and
	// returns an already closed ready channel; the webhook server is expected
	// to use pre-provisioned certificates from its CertDir.
	Disabled bool

	// Namespace of the Secret holding the generated CA and serving certificate.
	Namespace string
	// SecretName is the name of the Secret holding the generated CA and
	// serving certificate. The Secret must exist (it may be empty), either
	// created by the deployment manifests, and is expected
	// to be mounted at CertDir.
	SecretName string
	// ControllerName is passed to cert-controller. Defaults to "cert-rotator";
	// set a distinct name for each rotator registered in the same process.
	ControllerName string

	// ServiceName is the name of the webhook Service. When set, the in-cluster
	// names "<svc>.<ns>.svc" (used as DNSName if that is empty),
	// "<svc>.<ns>.svc.cluster.local" and "<svc>" are added to the certificate.
	ServiceName string
	// DNSName is the primary DNS name (CN/SAN) of the serving certificate.
	DNSName string
	// ExtraDNSNames are additional SANs for the serving certificate.
	ExtraDNSNames []string

	// CertDir is the directory the Secret is mounted at (tls.crt/tls.key). It
	// must match the webhook server's CertDir. Defaults to the controller-runtime
	// default "<os.TempDir()>/k8s-webhook-server/serving-certs".
	CertDir string

	// CAName is the common name of the generated CA. Defaults to "<SecretName>-ca".
	CAName string
	// CAOrganization is the organization of the generated CA (optional).
	CAOrganization string

	// ValidatingWebhooks are names of ValidatingWebhookConfigurations whose
	// caBundle is kept in sync with the generated CA.
	ValidatingWebhooks []string
	// MutatingWebhooks are names of MutatingWebhookConfigurations whose
	// caBundle is kept in sync with the generated CA.
	MutatingWebhooks []string

	// RequireLeaderElection runs the rotator only on the elected leader so
	// that replicas do not race on the Secret. Non-leader replicas then become
	// ready as soon as a matching, currently valid certificate/key pair appears
	// in CertDir. With false (the default), rotation runs on every replica.
	// This flag does not enable manager leader election: the manager must also
	// set ctrl.Options.LeaderElection and a shared LeaderElectionID. Without
	// manager leader election, every replica runs the rotator even when true.
	RequireLeaderElection bool
	// RestartOnSecretRefresh makes the process exit (os.Exit(0)) whenever the
	// rotator writes new certificates to the Secret, including the initial
	// generation, so that the pod restarts with the freshly mounted files
	// instead of waiting for the kubelet to sync the volume.
	RestartOnSecretRefresh bool
	// FieldOwner is the field manager used when updating webhook
	// configurations (optional).
	FieldOwner string
}

// Validate reports whether c is usable by [AddRotator]. A disabled config is
// always valid. Errors wrap [ErrInvalidConfig].
func (c Config) Validate() error {
	if c.Disabled {
		return nil
	}
	var errs []error
	if c.Namespace == "" {
		errs = append(errs, errors.New("namespace is required"))
	}
	if c.SecretName == "" {
		errs = append(errs, errors.New("secret name is required"))
	}
	if c.DNSName == "" && c.ServiceName == "" {
		errs = append(errs, errors.New("DNS name or service name is required"))
	}
	if slices.Contains(c.ValidatingWebhooks, "") {
		errs = append(errs, errors.New("validating webhook names must not be empty"))
	}
	if slices.Contains(c.MutatingWebhooks, "") {
		errs = append(errs, errors.New("mutating webhook names must not be empty"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, errors.Join(errs...))
	}
	return nil
}

// withDefaults returns a copy of c with defaults applied.
func (c Config) withDefaults() Config {
	if c.CertDir == "" {
		c.CertDir = filepath.Join(os.TempDir(), "k8s-webhook-server", "serving-certs")
	}
	if c.CAName == "" {
		c.CAName = c.SecretName + "-ca"
	}
	extra := slices.Clone(c.ExtraDNSNames)
	if c.ServiceName != "" {
		svc := c.ServiceName + "." + c.Namespace + ".svc"
		if c.DNSName == "" {
			c.DNSName = svc
		} else {
			extra = append(extra, svc)
		}
		extra = append(extra, svc+".cluster.local", c.ServiceName)
	}
	c.ExtraDNSNames = nil
	for _, n := range extra {
		if n != "" && n != c.DNSName && !slices.Contains(c.ExtraDNSNames, n) {
			c.ExtraDNSNames = append(c.ExtraDNSNames, n)
		}
	}
	return c
}

// webhooks maps the configured webhook names to cert-controller WebhookInfos
// (mutating first, then validating).
func (c Config) webhooks() []rotator.WebhookInfo {
	whs := make([]rotator.WebhookInfo, 0, len(c.MutatingWebhooks)+len(c.ValidatingWebhooks))
	for _, n := range c.MutatingWebhooks {
		whs = append(whs, rotator.WebhookInfo{Name: n, Type: rotator.Mutating})
	}
	for _, n := range c.ValidatingWebhooks {
		whs = append(whs, rotator.WebhookInfo{Name: n, Type: rotator.Validating})
	}
	return whs
}
