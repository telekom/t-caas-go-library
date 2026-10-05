// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// EnsureFinalizer adds finalizer to obj and persists it with a single JSON
// merge patch that carries obj's resourceVersion. It returns true if the
// finalizer was added and false if obj already had it (no request is sent).
//
// obj is updated in place: on success it holds the object returned by the API
// server, so later writes use the new resourceVersion. On error the in-memory
// finalizers are restored and the error is returned wrapped with %w; a
// conflict (apierrors.IsConflict) means obj is stale and the reconcile should
// be retried with a fresh read. Callers that want an in-place retry can use
// [Object] with a mutate func calling controllerutil.AddFinalizer instead.
func EnsureFinalizer(ctx context.Context, c client.Writer, obj client.Object, finalizer string) (bool, error) {
	return patchFinalizers(ctx, c, obj, finalizer, controllerutil.AddFinalizer)
}

// RemoveFinalizer removes finalizer from obj and persists it with a single
// JSON merge patch that carries obj's resourceVersion. It returns true if the
// finalizer was removed and false if obj did not have it (no request is sent).
//
// obj is updated in place as described for [EnsureFinalizer]. A NotFound error
// means the object is already gone; wrap the call with client.IgnoreNotFound
// when that is acceptable.
func RemoveFinalizer(ctx context.Context, c client.Writer, obj client.Object, finalizer string) (bool, error) {
	return patchFinalizers(ctx, c, obj, finalizer, controllerutil.RemoveFinalizer)
}

func patchFinalizers(
	ctx context.Context,
	c client.Writer,
	obj client.Object,
	finalizer string,
	edit func(client.Object, string) bool,
) (bool, error) {
	base, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return false, fmt.Errorf("deep copy of %T is not a client.Object", obj)
	}
	if !edit(obj, finalizer) {
		return false, nil
	}
	if err := c.Patch(ctx, obj, lockedMergeFrom(base)); err != nil {
		obj.SetFinalizers(base.GetFinalizers())
		return false, fmt.Errorf("patch finalizer %q on %T %s: %w", finalizer, obj, client.ObjectKeyFromObject(obj), err)
	}
	return true, nil
}
