// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package remoteclient

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SecretReference identifies a Secret and an explicit data key containing kubeconfig.
type SecretReference struct {
	Secret  types.NamespacedName
	DataKey string
}

// Resolver maps a cluster key to its credential Secret without consumer-specific CRDs.
// Implementations must be concurrency-safe and enforce their own reference policy.
type Resolver interface {
	Resolve(ctx context.Context, cluster types.NamespacedName) (SecretReference, error)
}

// ResolverFunc adapts a function to Resolver.
type ResolverFunc func(context.Context, types.NamespacedName) (SecretReference, error)

// Resolve implements Resolver.
func (f ResolverFunc) Resolve(ctx context.Context, key types.NamespacedName) (SecretReference, error) {
	return f(ctx, key)
}

// RefreshSecret resolves and loads credentials, tracking the dependency before
// reading the Secret so an update during loading or construction fences this refresh.
// Reader should be a live API reader when cached Secret reads are too stale.
func (r *Registry) RefreshSecret(ctx context.Context, key types.NamespacedName, reader client.Reader, resolver Resolver) error {
	version, err := r.begin(key)
	if err != nil {
		return err
	}
	if reader == nil || resolver == nil {
		return errors.New("reader and resolver are required")
	}
	ref, err := resolver.Resolve(ctx, key)
	if err != nil {
		return fmt.Errorf("resolve credential reference: %w", err)
	}
	if ref.Secret.Namespace == "" || ref.Secret.Name == "" || ref.DataKey == "" {
		return errors.New("secret namespace, name and data key are required")
	}
	r.mu.Lock()
	if r.generations[key] != version {
		r.mu.Unlock()
		return ErrSuperseded
	}
	if r.dependencies[key] == nil {
		r.dependencies[key] = make(map[types.NamespacedName]struct{})
	}
	r.dependencies[key][ref.Secret] = struct{}{}
	r.mu.Unlock()
	var secret corev1.Secret
	if err := reader.Get(ctx, ref.Secret, &secret); err != nil {
		return fmt.Errorf("load credential Secret: %w", err)
	}
	data, ok := secret.Data[ref.DataKey]
	if !ok {
		return errors.New("credential Secret is missing the kubeconfig data key")
	}
	return r.build(ctx, key, version, data, &ref.Secret)
}

// InvalidateSecret removes all clients and pending refreshes depending on secret.
// Call it on Secret create/update/delete events. Hooks run outside the lock.
// Dependency scanning is linear in the number of tracked clusters.
func (r *Registry) InvalidateSecret(secret types.NamespacedName) {
	r.mu.Lock()
	removed := make(map[types.NamespacedName]*entry)
	for cluster, dependency := range r.dependencies {
		if _, ok := dependency[secret]; ok {
			removed[cluster] = r.removeLocked(cluster)
		}
	}
	r.mu.Unlock()
	for key, old := range removed {
		r.notify(key, old)
	}
}
