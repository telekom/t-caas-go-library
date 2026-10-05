// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package patch provides small, generic helpers for conflict-safe writes of
// Kubernetes objects with controller-runtime.
//
// All writers send JSON merge patches that carry the resourceVersion of the
// object they were computed from (client.MergeFromWithOptimisticLock). A
// concurrent writer therefore makes the API server reject the patch with a
// 409 Conflict instead of silently overwriting the other change — important
// for merge patches, which replace lists such as status.conditions or
// metadata.finalizers as a whole.
//
//   - [Status] and [Object] run a read-mutate-patch cycle on the status
//     subresource or the main resource, retrying the whole cycle on conflict
//     so the mutation always applies to the latest live object.
//   - [EnsureFinalizer] and [RemoveFinalizer] add or remove a finalizer on an
//     in-memory object with a single optimistic-lock patch; a conflict is
//     returned so the reconciler can requeue.
//
// These adapters only combine client-go retry and controller-runtime patch
// and finalizer APIs for repeated consumer call sites. For snapshot/deferred
// patches and owned-condition merging, use github.com/fluxcd/pkg/runtime/patch
// directly. For Server-Side Apply, use generated apply configurations or
// client.ApplyConfigurationFromUnstructured.
package patch
