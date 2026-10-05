// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ssa

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
)

func TestLowerCamel(t *testing.T) {
	for kind, want := range map[string]string{
		"ClusterRole":        "clusterRole",
		"ClusterRoleBinding": "clusterRoleBinding",
		"ServiceAccount":     "serviceAccount",
		"RBACPolicy":         "rbacPolicy",
		"ABC":                "abc",
		"":                   "",
	} {
		if got := lowerCamel(kind); got != want {
			t.Errorf("lowerCamel(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestPatchApplyResultString(t *testing.T) {
	for result, want := range map[PatchApplyResult]string{
		PatchApplyResultSkipped: "skipped",
		PatchApplyResultCreated: "created",
		PatchApplyResultPatched: "patched",
		PatchApplyResult(99):    "unknown",
	} {
		if got := result.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", result, got, want)
		}
	}
}

func TestHasApplyPreconditions(t *testing.T) {
	tests := map[string]struct {
		ac   any
		want bool
	}{
		"none":                  {ac: corev1ac.ConfigMap("c", "ns"), want: false},
		"uid":                   {ac: corev1ac.ConfigMap("c", "ns").WithUID("u"), want: true},
		"resourceVersion":       {ac: corev1ac.ConfigMap("c", "ns").WithResourceVersion("1"), want: true},
		"empty resourceVersion": {ac: corev1ac.ConfigMap("c", "ns").WithResourceVersion(""), want: true},
		"no metadata":           {ac: &corev1ac.ConfigMapApplyConfiguration{}, want: false},
		"unmarshalable":         {ac: func() {}, want: true},
		"not an object":         {ac: []string{"x"}, want: true},
	}
	for name, tt := range tests {
		if got := hasApplyPreconditions(tt.ac); got != tt.want {
			t.Errorf("%s: hasApplyPreconditions() = %v, want %v", name, got, tt.want)
		}
	}
}

func TestIsNil(t *testing.T) {
	var cm *corev1ac.ConfigMapApplyConfiguration
	if !isNil(nil) || !isNil(cm) || isNil(corev1ac.ConfigMap("c", "ns")) || isNil(1) {
		t.Fatal("isNil returned an unexpected result")
	}
}

func TestSortByJSON(t *testing.T) {
	items := []map[string]string{{"b": "2"}, {"a": "1"}}
	if err := sortByJSON(items); err != nil {
		t.Fatal(err)
	}
	if items[0]["a"] != "1" {
		t.Errorf("SortByJSON did not sort: %v", items)
	}
	bad := []any{"b", func() {}}
	if err := sortByJSON(bad); err == nil {
		t.Error("SortByJSON accepted an unencodable item")
	}
	if bad[0] != "b" {
		t.Error("SortByJSON modified the list on error")
	}
}

func TestApplyFieldMaps(t *testing.T) {
	owned := corev1ac.ConfigMap("c", "ns").
		WithResourceVersion("7").
		WithOwnerReferences(ownerRef("b"), ownerRef("a"))
	desired := corev1ac.ConfigMap("c", "ns").WithOwnerReferences(ownerRef("a"), ownerRef("b"))
	ownedFields, desiredFields, ok := applyFieldMaps(owned, desired, nil)
	if !ok {
		t.Fatal("applyFieldMaps failed")
	}
	if !applyFieldMapSubset(ownedFields, desiredFields) || !applyFieldMapSubset(desiredFields, ownedFields) {
		t.Errorf("normalized maps differ: %v vs %v", ownedFields, desiredFields)
	}
	if _, _, ok := applyFieldMaps(func() {}, desired, nil); ok {
		t.Error("applyFieldMaps accepted an unencodable owned value")
	}
	if _, _, ok := applyFieldMaps(owned, func() {}, nil); ok {
		t.Error("applyFieldMaps accepted an unencodable desired value")
	}
	if _, _, ok := applyFieldMaps([]int{1}, desired, nil); ok {
		t.Error("applyFieldMaps accepted a non-object")
	}
	called := 0
	if _, _, ok := applyFieldMaps(owned, desired, func(map[string]any) { called++ }); !ok || called != 2 {
		t.Errorf("normalize hook called %d times, want 2", called)
	}
}

func TestApplyFieldMapSubset(t *testing.T) {
	desired := map[string]any{"a": "1", "m": map[string]any{"x": "1", "y": "2"}}
	tests := map[string]struct {
		owned map[string]any
		want  bool
	}{
		"empty":          {owned: map[string]any{}, want: true},
		"equal":          {owned: desired, want: true},
		"nested subset":  {owned: map[string]any{"m": map[string]any{"x": "1"}}, want: true},
		"extra key":      {owned: map[string]any{"b": "2"}, want: false},
		"extra nested":   {owned: map[string]any{"m": map[string]any{"z": "1"}}, want: false},
		"different leaf": {owned: map[string]any{"a": "2"}, want: false},
		"map vs scalar":  {owned: map[string]any{"a": map[string]any{}}, want: false},
	}
	for name, tt := range tests {
		if got := applyFieldMapSubset(tt.owned, desired); got != tt.want {
			t.Errorf("%s: applyFieldMapSubset() = %v, want %v", name, got, tt.want)
		}
	}
}

func TestMatchesJSON(t *testing.T) {
	existing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p", Namespace: "ns", ResourceVersion: "3",
			Labels: map[string]string{"app": "x", "foreign": "y"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "c", Image: "img", TerminationMessagePath: "/dev/termination-log",
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	pod := func() *corev1ac.PodApplyConfiguration {
		return corev1ac.Pod("p", "ns").WithLabels(map[string]string{"app": "x"}).
			WithSpec(corev1ac.PodSpec().WithContainers(corev1ac.Container().WithName("c").WithImage("img")))
	}
	tests := map[string]struct {
		desired *corev1ac.PodApplyConfiguration
		want    bool
	}{
		"subset with server defaults": {desired: pod(), want: true},
		"status differs":              {desired: pod().WithStatus(corev1ac.PodStatus().WithPhase(corev1.PodFailed)), want: false},
		"different label":             {desired: pod().WithLabels(map[string]string{"app": "z"}), want: false},
		"missing label":               {desired: pod().WithLabels(map[string]string{"new": "1"}), want: false},
		"different list element": {desired: corev1ac.Pod("p", "ns").WithSpec(corev1ac.PodSpec().
			WithContainers(corev1ac.Container().WithName("c").WithImage("other"))), want: false},
		"different list length": {desired: corev1ac.Pod("p", "ns").WithSpec(corev1ac.PodSpec().
			WithContainers(corev1ac.Container().WithName("c"), corev1ac.Container().WithName("d"))), want: false},
		"nil desired": {desired: nil, want: false},
	}
	for name, tt := range tests {
		if got := MatchesJSON(existing, tt.desired); got != tt.want {
			t.Errorf("%s: MatchesJSON() = %v, want %v", name, got, tt.want)
		}
	}
	if MatchesJSON(existing, func() {}) || MatchesJSON(func() {}, pod()) || MatchesJSON(existing, []int{1}) {
		t.Error("MatchesJSON matched an unencodable or non-object value")
	}
	if jsonValueCovers("x", map[string]any{}) || jsonValueCovers("x", []any{}) {
		t.Error("jsonValueCovers matched mismatching JSON types")
	}
}

func TestJSONIntegerPrecision(t *testing.T) {
	existing := map[string]any{"spec": map[string]any{"count": int64(9007199254740992)}}
	equal := map[string]any{"spec": map[string]any{"count": int64(9007199254740992)}}
	different := map[string]any{"spec": map[string]any{"count": int64(9007199254740993)}}
	if !MatchesJSON(existing, equal) || MatchesJSON(existing, different) {
		t.Error("MatchesJSON lost integer precision")
	}
	ownedFields, desiredFields, ok := applyFieldMaps(existing, different, nil)
	if !ok || applyFieldMapSubset(ownedFields, desiredFields) || applyFieldMapSubset(desiredFields, ownedFields) {
		t.Error("ownership comparison lost integer precision")
	}
}

func TestMatchesJSONIncludesStatus(t *testing.T) {
	current := map[string]any{"apiVersion": "example.com/v1", "kind": "Widget", "status": map[string]any{"phase": "Pending"}}
	equal := map[string]any{"status": map[string]any{"phase": "Pending"}}
	changed := map[string]any{"status": map[string]any{"phase": "Ready"}}
	if !MatchesJSON(current, equal) || MatchesJSON(current, changed) {
		t.Error("status changes must be compared: CRDs without a status subresource apply them on the main endpoint")
	}
}

func ownerRef(uid string) *metav1ac.OwnerReferenceApplyConfiguration {
	return metav1ac.OwnerReference().WithAPIVersion("v1").WithKind("ConfigMap").WithName(uid).WithUID(types.UID(uid))
}
