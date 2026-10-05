// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MutateFunc edits obj in memory and reports whether anything changed.
//
// It must only modify obj (reads through other clients are fine). It must not
// write obj to the API server itself: the patch base is captured before
// MutateFunc runs, so a write that advances the resourceVersion makes every
// patch conflict. It must not change the object's key, UID, or GVK.
// Status callbacks must only modify status; other changes are rejected.
// Returning changed=false skips the patch.
type MutateFunc[T client.Object] func(obj T) (changed bool, err error)

// Status runs a read-mutate-patch cycle against the status subresource of the
// object identified by key:
//
//  1. a fresh object from newObj is read through reader (nil means c);
//  2. mutate edits it in place and reports whether anything changed;
//  3. if changed, the difference is sent to the status subresource as a JSON
//     merge patch carrying the read resourceVersion, so a concurrent writer
//     makes the API server reject the patch with a conflict.
//
// The whole cycle is retried with [retry.RetryOnConflict] using backoff, so
// mutate always works on the latest live object. Pass [retry.DefaultRetry] or
// [retry.DefaultBackoff] to retry, or wait.Backoff{Steps: 1} for a single
// attempt that returns the conflict to the caller. A mutate error that is
// itself a conflict is retried like a patch conflict; other errors stop the
// cycle immediately.
//
// Use a live reader (for example manager.GetAPIReader()) when the cached
// client may lag behind; otherwise every retry may re-read the same stale
// object until the cache catches up.
//
// Errors from mutate are returned as-is. Read and patch errors are wrapped
// with %w, so apierrors.IsNotFound and apierrors.IsConflict keep working.
//
// On success the returned object is the object as returned by the API server
// after the patch. When mutate reported no change, no request is sent and the
// returned object is the object as read, including any in-memory edits mutate
// made (they are not persisted). On error the zero value of T is returned.
func Status[T client.Object](
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	backoff wait.Backoff,
	key client.ObjectKey,
	newObj func() T,
	mutate MutateFunc[T],
) (T, error) {
	return readMutatePatch(ctx, c, reader, backoff, key, newObj, mutate, true)
}

// Object is the main-resource counterpart of [Status]: it runs the same
// retried read-mutate-patch cycle but sends the optimistic-lock merge patch to
// the object itself (metadata, spec, data, …) instead of its status
// subresource. Changes mutate makes to the status stanza of types with a
// status subresource are ignored by the API server.
//
// Semantics of reader, backoff, mutate, errors and the returned object are the
// same as for [Status].
func Object[T client.Object](
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	backoff wait.Backoff,
	key client.ObjectKey,
	newObj func() T,
	mutate MutateFunc[T],
) (T, error) {
	return readMutatePatch(ctx, c, reader, backoff, key, newObj, mutate, false)
}

func readMutatePatch[T client.Object](
	ctx context.Context,
	c client.Client,
	reader client.Reader,
	backoff wait.Backoff,
	key client.ObjectKey,
	newObj func() T,
	mutate MutateFunc[T],
	status bool,
) (T, error) {
	if reader == nil {
		reader = c
	}
	var result T
	err := retry.RetryOnConflict(backoff, func() error {
		obj := newObj()
		if err := reader.Get(ctx, key, obj); err != nil {
			return fmt.Errorf("get %T %s: %w", obj, key, err)
		}
		base, ok := obj.DeepCopyObject().(client.Object)
		if !ok {
			return fmt.Errorf("deep copy of %T is not a client.Object", obj)
		}
		changed, err := mutate(obj)
		if err != nil {
			return err
		}
		if client.ObjectKeyFromObject(obj) != client.ObjectKeyFromObject(base) ||
			obj.GetUID() != base.GetUID() ||
			obj.GetObjectKind().GroupVersionKind() != base.GetObjectKind().GroupVersionKind() {
			return fmt.Errorf("mutate %T %s changed object identity", obj, key)
		}
		if status {
			if err := validateStatusMutation(base, obj); err != nil {
				return err
			}
		}
		if !changed {
			result = obj
			return nil
		}

		p := lockedMergeFrom(base)
		if status {
			err = c.Status().Patch(ctx, obj, p)
		} else {
			err = c.Patch(ctx, obj, p)
		}
		if err != nil {
			return fmt.Errorf("patch %T %s (status=%t): %w", obj, key, status, err)
		}
		result = obj
		return nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return result, nil
}

func lockedMergeFrom(base client.Object) client.Patch {
	return client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
}

func validateStatusMutation(base, obj client.Object) error {
	data, err := client.MergeFrom(base).Data(obj)
	if err != nil {
		return fmt.Errorf("compute status mutation: %w", err)
	}
	var changes map[string]json.RawMessage
	if err := json.Unmarshal(data, &changes); err != nil {
		return fmt.Errorf("decode status mutation: %w", err)
	}
	for field := range changes {
		if field != "status" {
			return fmt.Errorf("status mutation changed non-status field %q", field)
		}
	}
	return nil
}
