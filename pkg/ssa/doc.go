// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package ssa implements skip-if-unchanged Server-Side Apply (SSA) for
// controller-runtime clients.
//
// # Why skip unchanged applies
//
// SSA is idempotent: re-applying the same configuration does not change the
// object. It is not free, though. Every apply is a PATCH request that passes
// authentication, authorization, all mutating and validating admission
// webhooks and policies, a structured merge and an etcd round-trip, even when
// the result is a no-op. Controllers that re-apply every managed object on
// every reconcile therefore produce thousands of no-op requests in large
// clusters and load admission webhooks with work that has no effect.
//
// [Applier] reads the live object first, normally from the controller-runtime
// informer cache (a local, free operation), and only sends the apply when it
// would change something: a desired value differs, or the field manager's
// ownership must change. Otherwise [Applier.PatchApply] returns
// [PatchApplyResultSkipped] without contacting the API server.
// An uncached client still sends the preflight Get request.
//
// # Apply decision
//
// For a descriptor Applier[T, AC], PatchApply(ctx, c, ac, alwaysApply, opts...):
//
//  1. validates the name (and namespace when Namespaced) and requires a
//     client.FieldOwner in opts;
//  2. reads the live object; when it does not exist, applies and returns
//     [PatchApplyResultCreated];
//  3. returns [PatchApplyResultSkipped] only when Matches reports that every
//     desired value is present, alwaysApply is false, ac carries no uid or
//     resourceVersion precondition, and the ownership check allows it;
//  4. otherwise applies and returns
//     [PatchApplyResultPatched].
//
// Results classify the preflight branch, not the server's eventual mutation.
// Dry-runs still return Created/Patched; concurrent creation or deletion can
// change whether a main-resource apply actually creates or updates an object.
//
// # Ownership semantics
//
// Equal values are not enough to skip an apply. SSA also tracks which field
// manager owns which field, and an apply has two effects beyond setting
// values:
//
//   - Pruning: fields the manager owned in its previous apply but no longer
//     declares are removed. If the desired configuration drops a field, the
//     live object still matches every desired value, yet the apply is needed
//     to remove the field.
//   - Reclaiming: a forced apply (client.ForceOwnership) takes ownership of
//     declared fields from other managers. If another manager force-applied
//     the same values, the live object matches, yet a forced apply would make
//     this manager a (co-)owner again so it can later prune or change them.
//
// The Applier therefore compares the fields the manager currently owns,
// obtained with the client-go / applyconfiguration-gen Extract function, with
// the desired configuration (after normalizing server-populated metadata,
// owner reference order and the NormalizeFields hook):
//
//   - A forced apply is skipped only when
//     the owned fields equal the desired fields; an unforced apply is skipped
//     unless the manager still owns a field that is no longer desired (and
//     must be pruned).
//
// When managedFields are unavailable (for example a cache that strips them)
// or cannot be decoded, the apply is sent: the Applier never guesses that an
// apply is redundant. Dry-run applies and applies
// carrying uid/resourceVersion preconditions are never skipped, so the API
// server always evaluates them.
// Apply errors are always propagated, including conflicts; callers requeue
// using their existing controller-runtime reconciliation policy.
//
// # Defining a descriptor
//
// An Applier is a plain value describing one object type. Only Kind, New,
// Matches and Extract are required:
//
//	var configMaps = ssa.Applier[*corev1.ConfigMap, *corev1ac.ConfigMapApplyConfiguration]{
//		Kind:       "ConfigMap",
//		Namespaced: true,
//		New:        func() *corev1.ConfigMap { return &corev1.ConfigMap{} },
//		Matches:    ssa.MatchesJSON[*corev1.ConfigMap, *corev1ac.ConfigMapApplyConfiguration],
//		Extract:    corev1ac.ExtractConfigMap,
//	}
//
//	result, err := configMaps.PatchApply(ctx, c,
//		corev1ac.ConfigMap("settings", "default").WithData(data), false,
//		client.FieldOwner("my-operator"), client.ForceOwnership)
//
// Matches must compare only fields the configuration declares; [MatchesJSON]
// is a generic implementation. Build a fresh apply configuration for every
// call: client.Apply writes the server response, including resourceVersion
// and uid, back into it, and PatchApply would then treat those as
// preconditions. The field manager is always supplied by the caller via
// client.FieldOwner; the package has no default.
//
// # Status subresources
//
// [StatusApplier] applies the status subresource with ForceOwnership (a
// controller is the sole owner of the status it computes) and skips the
// request when the cached status already equals the desired one.
// [ApplyStatus] applies a status unconditionally.
// Both require a non-empty field owner, including when status is unchanged.
//
// # Use upstream instead for general manifest reconciliation
//
// Prefer github.com/fluxcd/pkg/ssa.ResourceManager for arbitrary manifests,
// server-side defaulting, drift detection, metadata cleanup, staged apply,
// waiting and garbage collection. Its Apply/ApplyAll methods compare the
// live object with a server-side dry-run result: even unchanged resources
// send a dry-run PATCH through authorization and admission. They also
// always use ForceOwnership; ApplyOptions.Force controls recreation for
// immutable fields, not managed-field takeover.
//
// This package retains only the cache-based comparison and ownership gate
// shared by auth-operator and k8s-breakglass. Writes delegate directly to
// controller-runtime's client.Apply or SubResource("status").Apply. It is
// not a substitute for Flux's general-purpose resource manager. RBAC
// comparators, label cleanup and canonicalization stay in consumers or use
// upstream APIs; no single-consumer descriptor wrappers are provided.
// The no-op admission-traffic reduction is exercised by auth-operator PRs
// #577 (counted binding applies and isolated Kyverno coverage) and #580
// (generic descriptors preserving the cache/ownership decision).
//
// Cached equality is not a server-side dry-run: it cannot account for
// changed admission policies, revoked permissions or server defaulting.
// Use alwaysApply when authorization must be rechecked; use Flux when the
// server must evaluate the resulting state. Keep managedFields in the
// cache and ensure Matches/NormalizeFields model the resource's schema.
// Unforced skipped applies do not acquire shared ownership of matching
// foreign fields. StatusApplier assumes the controller exclusively owns
// the status it compares; it does not reclaim ownership on equal status.
//
// The package depends only on the Go standard library, k8s.io/apimachinery,
// k8s.io/client-go and sigs.k8s.io/controller-runtime.
package ssa
