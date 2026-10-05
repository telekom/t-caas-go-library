// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ssa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// PatchApplyResult identifies the preflight branch of an apply-or-skip operation,
// not a confirmed server-side mutation. Dry-runs use the same result values,
// and concurrent creation/deletion can change what the server actually does.
type PatchApplyResult int

const (
	// PatchApplyResultSkipped means the resource was already up-to-date (no apply request made).
	PatchApplyResultSkipped PatchApplyResult = iota
	// PatchApplyResultCreated means preflight Get returned NotFound and apply
	// succeeded. It may be a dry-run or an update of a concurrently created object.
	PatchApplyResultCreated
	// PatchApplyResultPatched means an apply was sent for a preflight cache hit,
	// or for status. It does not guarantee a persisted mutation; a concurrent
	// deletion may also cause main-resource apply to create instead.
	PatchApplyResultPatched
)

// String returns a human-readable label for the result.
func (r PatchApplyResult) String() string {
	switch r {
	case PatchApplyResultSkipped:
		return "skipped"
	case PatchApplyResultCreated:
		return "created"
	case PatchApplyResultPatched:
		return "patched"
	default:
		return "unknown"
	}
}

// ApplyConfiguration is a typed SSA apply configuration that exposes its
// object identity. All client-go and applyconfiguration-gen generated
// top-level apply configurations satisfy it.
type ApplyConfiguration interface {
	runtime.ApplyConfiguration
	GetName() *string
	GetNamespace() *string
}

// Applier describes how to skip-if-unchanged Server-Side Apply one object
// type. It reads the live object (typically from the informer cache), and
// only sends an SSA apply when the desired values differ or when the field
// manager's ownership must change (reclaim or prune).
//
// Only Kind, New, Matches and Extract are required; NormalizeFields is an
// optional hook for schema-specific comparison.
type Applier[T client.Object, AC ApplyConfiguration] struct {
	// Kind is used in errors and logs, e.g. "ClusterRole".
	Kind string
	// Namespaced requires a namespace on the desired apply configuration.
	Namespaced bool
	// New returns an empty object to read the live state into.
	New func() T
	// Matches reports whether existing already has every desired value.
	Matches func(existing T, desired AC) bool
	// Extract returns the fields owned by fieldManager, e.g. rbacv1ac.ExtractRole.
	Extract func(existing T, fieldManager string) (AC, error)
	// NormalizeFields canonicalizes the JSON form of owned and desired fields
	// before comparison, e.g. sorting lists the API server treats as sets.
	// Server-populated metadata and owner reference order are always ignored.
	NormalizeFields func(fields map[string]any)
}

// PatchApply reads the live object and applies ac via SSA only when needed.
// opts are forwarded to every apply and must include a client.FieldOwner.
// When alwaysApply is true the apply is sent even when the live object
// already matches, e.g. when the apply identity is itself an authorization
// boundary that the API server must recheck.
//
// Dry-run applies and applies whose ac carries
// UID/resourceVersion preconditions are never skipped. Pass a fresh ac on
// every call: client.Apply writes the server response, including
// resourceVersion and uid, back into it.
func (a Applier[T, AC]) PatchApply(
	ctx context.Context,
	c client.Client,
	ac AC,
	alwaysApply bool,
	opts ...client.ApplyOption,
) (PatchApplyResult, error) {
	target, options, err := a.target(ac, opts)
	if err != nil {
		return 0, err
	}

	existing := a.New()
	if err := c.Get(ctx, target.key, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return 0, fmt.Errorf("get %s %s: %w", a.Kind, target.ref, err)
		}
		return a.create(ctx, c, ac, target, opts)
	}

	force := options.Force != nil && *options.Force
	if !alwaysApply && a.Matches(existing, ac) &&
		a.canSkip(existing, ac, options, force) {
		log.FromContext(ctx).V(3).Info(a.Kind+" unchanged, skipping SSA apply", target.logKV...)
		return PatchApplyResultSkipped, nil
	}

	if applyErr := c.Apply(ctx, ac, opts...); applyErr != nil {
		return 0, fmt.Errorf("patch %s %s: %w", a.Kind, target.ref, applyErr)
	}
	return PatchApplyResultPatched, nil
}

// applyTarget identifies the object an apply configuration targets.
type applyTarget struct {
	key   types.NamespacedName
	ref   string // "name" or "namespace/name" for errors
	logKV []any
}

func (a Applier[T, AC]) target(ac AC, opts []client.ApplyOption) (applyTarget, *client.ApplyOptions, error) {
	kind := lowerCamel(a.Kind)
	if isNil(ac) || ac.GetName() == nil {
		return applyTarget{}, nil, fmt.Errorf("%s ApplyConfiguration must have a name", kind)
	}
	target := applyTarget{key: types.NamespacedName{Name: *ac.GetName()}}
	if target.key.Name == "" {
		return applyTarget{}, nil, fmt.Errorf("%s ApplyConfiguration name must not be empty", kind)
	}
	target.ref = target.key.Name
	target.logKV = []any{kind, target.key.Name}
	if a.Namespaced {
		if ac.GetNamespace() == nil || *ac.GetNamespace() == "" {
			return applyTarget{}, nil, fmt.Errorf("%s ApplyConfiguration must have a namespace", kind)
		}
		target.key.Namespace = *ac.GetNamespace()
		target.ref = target.key.Namespace + "/" + target.key.Name
		target.logKV = append(target.logKV, "namespace", target.key.Namespace)
	}
	options := (&client.ApplyOptions{}).ApplyOptions(opts)
	if strings.TrimSpace(options.FieldManager) == "" {
		return applyTarget{}, nil, errors.New("fieldOwner must not be empty")
	}
	return target, options, nil
}

func (a Applier[T, AC]) create(
	ctx context.Context,
	c client.Client,
	ac AC,
	target applyTarget,
	opts []client.ApplyOption,
) (PatchApplyResult, error) {
	if err := c.Apply(ctx, ac, opts...); err != nil {
		return 0, fmt.Errorf("create %s %s: %w", a.Kind, target.ref, err)
	}
	return PatchApplyResultCreated, nil
}

func (a Applier[T, AC]) canSkip(existing T, ac AC, options *client.ApplyOptions, force bool) bool {
	if hasApplyPreconditions(ac) {
		return false
	}
	if len(options.DryRun) != 0 || len(existing.GetManagedFields()) == 0 {
		return false
	}
	owned, err := a.Extract(existing, options.FieldManager)
	if err != nil {
		return false
	}
	ownedFields, desiredFields, ok := applyFieldMaps(owned, ac, a.NormalizeFields)
	if !ok {
		return false
	}
	if force {
		return reflect.DeepEqual(ownedFields, desiredFields)
	}
	return applyFieldMapSubset(ownedFields, desiredFields)
}

func hasApplyPreconditions(ac any) bool {
	data, err := json.Marshal(ac)
	if err != nil {
		return true
	}
	var object struct {
		Metadata *struct {
			UID             *string `json:"uid"`
			ResourceVersion *string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return true
	}
	return object.Metadata != nil && (object.Metadata.UID != nil || object.Metadata.ResourceVersion != nil)
}

func applyFieldMaps(owned, desired any, normalize func(map[string]any)) (ownedFields, desiredFields map[string]any, ok bool) {
	ownedFields, ok = toFieldMap(owned)
	if !ok {
		return nil, nil, false
	}
	desiredFields, ok = toFieldMap(desired)
	if !ok {
		return nil, nil, false
	}
	for _, fields := range []map[string]any{ownedFields, desiredFields} {
		normalizeMetadataFields(fields)
		if normalize != nil {
			normalize(fields)
		}
	}
	return ownedFields, desiredFields, true
}

func normalizeMetadataFields(fields map[string]any) {
	metadata, ok := fields["metadata"].(map[string]any)
	if !ok {
		return
	}
	for _, key := range []string{"uid", "resourceVersion", "creationTimestamp", "generation", "managedFields"} {
		delete(metadata, key)
	}
	// ownerReferences is an SSA map-list keyed by uid, so order is irrelevant.
	if refs, ok := metadata["ownerReferences"].([]any); ok {
		_ = sortByJSON(refs)
	}
}

func applyFieldMapSubset(owned, desired map[string]any) bool {
	for key, value := range owned {
		target, ok := desired[key]
		if !ok {
			return false
		}
		if nested, ok := value.(map[string]any); ok {
			targetNested, ok := target.(map[string]any)
			if !ok || !applyFieldMapSubset(nested, targetNested) {
				return false
			}
		} else if !reflect.DeepEqual(value, target) {
			return false
		}
	}
	return true
}

// sortByJSON sorts items in place by their JSON encoding, giving a stable,
// canonical order to lists the API server treats as sets (for example RBAC
// subjects or owner references). It leaves the list untouched and returns an
// error when any item cannot be encoded.
func sortByJSON[T any](items []T) error {
	type keyedItem struct {
		key   string
		value T
	}
	keyed := make([]keyedItem, len(items))
	for i, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal ApplyConfiguration list item: %w", err)
		}
		keyed[i] = keyedItem{key: string(encoded), value: item}
	}
	slices.SortFunc(keyed, func(a, b keyedItem) int {
		return strings.Compare(a.key, b.key)
	})
	for i := range keyed {
		items[i] = keyed[i].value
	}
	return nil
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	value := reflect.ValueOf(v)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

// lowerCamel lowercases the leading word of a Kind for log keys and error
// messages: "ClusterRole" -> "clusterRole", "RBACPolicy" -> "rbacPolicy".
func lowerCamel(kind string) string {
	runes := []rune(kind)
	upper := 0
	for upper < len(runes) && unicode.IsUpper(runes[upper]) {
		upper++
	}
	if upper > 1 && upper < len(runes) {
		upper--
	}
	for i := range upper {
		runes[i] = unicode.ToLower(runes[i])
	}
	return string(runes)
}
