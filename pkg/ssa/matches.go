// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ssa

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// MatchesJSON is a generic [Applier.Matches] implementation. It reports
// whether every value declared by desired is present with the same value in
// existing, comparing their JSON encodings:
//
//   - objects match when every desired key is present in existing with a
//     matching value; extra keys in existing (server defaults, fields of
//     other managers) are ignored;
//   - lists match when they have the same length and every desired element
//     matches the existing element at the same index;
//   - scalars must be equal; numbers retain their exact JSON representation.
//
// The top-level apiVersion and kind keys are ignored because typed objects
// read through controller-runtime usually carry no type metadata. Status is
// compared: CRDs without a status subresource accept it on the main endpoint.
//
// Removing a field from desired cannot be detected by value comparison; the
// [Applier] ownership check (Extract) covers that. Desired values must be in
// the canonical form the API server stores (for example "1" rather than
// "1000m" for quantities), otherwise MatchesJSON never reports a match and
// every call applies. Write a typed comparator when that is not practical or
// when the type has fields owned by another controller that MatchesJSON would
// compare (such as rules of aggregated ClusterRoles).
func MatchesJSON[T, AC any](existing T, desired AC) bool {
	existingFields, ok := toFieldMap(existing)
	if !ok {
		return false
	}
	desiredFields, ok := toFieldMap(desired)
	if !ok {
		return false
	}
	for _, key := range []string{"apiVersion", "kind"} {
		delete(desiredFields, key)
	}
	return jsonValueCovers(existingFields, desiredFields)
}

func toFieldMap(v any) (map[string]any, bool) {
	if isNil(v) {
		return nil, false
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

// jsonValueCovers reports whether existing contains every value of desired.
func jsonValueCovers(existing, desired any) bool {
	switch d := desired.(type) {
	case map[string]any:
		e, ok := existing.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range d {
			current, ok := e[key]
			if !ok || !jsonValueCovers(current, value) {
				return false
			}
		}
		return true
	case []any:
		e, ok := existing.([]any)
		if !ok || len(e) != len(d) {
			return false
		}
		for i := range d {
			if !jsonValueCovers(e[i], d[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(existing, desired)
	}
}
