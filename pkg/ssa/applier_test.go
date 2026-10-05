// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package ssa_test

import (
	"context"
	"errors"
	"maps"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telekom/t-caas-go-library/pkg/ssa"
)

const (
	testNamespace = "default"
	owner         = "test-operator"
	foreignOwner  = "external-agent"
)

var configMapApplier = ssa.Applier[*corev1.ConfigMap, *corev1ac.ConfigMapApplyConfiguration]{
	Kind:       "ConfigMap",
	Namespaced: true,
	New:        func() *corev1.ConfigMap { return &corev1.ConfigMap{} },
	Matches:    ssa.MatchesJSON[*corev1.ConfigMap, *corev1ac.ConfigMapApplyConfiguration],
	Extract:    corev1ac.ExtractConfigMap,
}

func desiredCM(name string, data map[string]string) *corev1ac.ConfigMapApplyConfiguration {
	return corev1ac.ConfigMap(name, testNamespace).
		WithLabels(map[string]string{"app": "test"}).
		WithData(data)
}

func getCM(name string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{}
	ExpectWithOffset(1, k8sClient.Get(testCtx, types.NamespacedName{Name: name, Namespace: testNamespace}, cm)).To(Succeed())
	return cm
}

func managedBy(obj metav1.Object, manager string) bool {
	return slices.ContainsFunc(obj.GetManagedFields(), func(entry metav1.ManagedFieldsEntry) bool {
		return entry.Manager == manager && entry.Operation == metav1.ManagedFieldsOperationApply
	})
}

var (
	forced   = []client.ApplyOption{client.FieldOwner(owner), client.ForceOwnership}
	unforced = []client.ApplyOption{client.FieldOwner(owner)}
)

var _ = Describe("Applier", func() {
	apply := func(c client.Client, applier ssa.Applier[*corev1.ConfigMap, *corev1ac.ConfigMapApplyConfiguration],
		ac *corev1ac.ConfigMapApplyConfiguration, opts []client.ApplyOption,
	) ssa.PatchApplyResult {
		GinkgoHelper()
		result, err := applier.PatchApply(testCtx, c, ac, false, opts...)
		Expect(err).NotTo(HaveOccurred())
		return result
	}
	create := func(name string, data map[string]string) {
		GinkgoHelper()
		Expect(apply(k8sClient, configMapApplier, desiredCM(name, data), forced)).To(Equal(ssa.PatchApplyResultCreated))
	}

	It("creates a missing object and skips it while unchanged", func() {
		name := "applier-unchanged"
		c := counting()
		Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}), forced)).
			To(Equal(ssa.PatchApplyResultCreated))
		for _, opts := range [][]client.ApplyOption{forced, unforced, forced} {
			Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}), opts)).
				To(Equal(ssa.PatchApplyResultSkipped))
		}
		Expect(c.ApplyCalls).To(Equal(1))
		Expect(managedBy(getCM(name), owner)).To(BeTrue())
	})

	It("patches drifted values", func() {
		name := "applier-drift"
		create(name, map[string]string{"a": "1"})
		cm := getCM(name)
		cm.Data["a"] = "drifted"
		Expect(k8sClient.Update(testCtx, cm)).To(Succeed())
		c := counting()
		Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}), forced)).
			To(Equal(ssa.PatchApplyResultPatched))
		Expect(c.ApplyCalls).To(Equal(1))
		Expect(getCM(name).Data).To(Equal(map[string]string{"a": "1"}))
	})

	It("claims shared ownership on forced apply but skips unforced matching values", func() {
		name := "applier-shared"
		Expect(k8sClient.Apply(testCtx, desiredCM(name, map[string]string{"a": "1"}),
			client.FieldOwner(foreignOwner), client.ForceOwnership)).To(Succeed())
		c := counting()
		Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}), unforced)).
			To(Equal(ssa.PatchApplyResultSkipped))
		Expect(c.ApplyCalls).To(BeZero())
		Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}), forced)).
			To(Equal(ssa.PatchApplyResultPatched))
		cm := getCM(name)
		Expect(managedBy(cm, owner)).To(BeTrue())
		Expect(managedBy(cm, foreignOwner)).To(BeTrue())
		for _, opts := range [][]client.ApplyOption{forced, unforced} {
			Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}), opts)).
				To(Equal(ssa.PatchApplyResultSkipped))
		}
		Expect(c.ApplyCalls).To(Equal(1))
	})

	It("repairs foreign force-applies once while preserving foreign labels", func() {
		name := "applier-repair"
		create(name, map[string]string{"a": "1"})
		Expect(k8sClient.Apply(testCtx, corev1ac.ConfigMap(name, testNamespace).
			WithLabels(map[string]string{"foreign": "keep"}).
			WithData(map[string]string{"a": "intruder"}),
			client.FieldOwner(foreignOwner), client.ForceOwnership)).To(Succeed())
		c := counting()
		Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}), forced)).
			To(Equal(ssa.PatchApplyResultPatched))
		Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}), forced)).
			To(Equal(ssa.PatchApplyResultSkipped))
		Expect(c.ApplyCalls).To(Equal(1))
		Expect(getCM(name).Labels).To(HaveKeyWithValue("foreign", "keep"))
	})

	DescribeTable("prunes a previously owned field although desired values match",
		func(name string, opts []client.ApplyOption) {
			create(name, map[string]string{"a": "1", "b": "2"})
			c := counting()
			Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}), opts)).
				To(Equal(ssa.PatchApplyResultPatched))
			Expect(c.ApplyCalls).To(Equal(1))
			Expect(getCM(name).Data).To(Equal(map[string]string{"a": "1"}))
		},
		Entry("forced", "applier-prune-forced", forced),
		Entry("unforced", "applier-prune-unforced", unforced),
	)

	DescribeTable("never skips dry-run applies",
		func(name string, opts []client.ApplyOption) {
			create(name, map[string]string{"a": "1"})
			c := counting()
			Expect(apply(c, configMapApplier, desiredCM(name, map[string]string{"a": "1"}),
				append(opts, client.DryRunAll))).To(Equal(ssa.PatchApplyResultPatched))
			Expect(c.ApplyCalls).To(Equal(1))
		},
		Entry("forced", "applier-dryrun-forced", forced),
		Entry("unforced", "applier-dryrun-unforced", unforced),
	)

	DescribeTable("forwards preconditions to the API server without hiding their errors",
		func(name string, precondition func(*corev1ac.ConfigMapApplyConfiguration), rejected func(error) bool) {
			create(name, map[string]string{"a": "1"})
			ac := desiredCM(name, map[string]string{"a": "1"})
			precondition(ac)
			c := counting()
			_, err := configMapApplier.PatchApply(testCtx, c, ac, false, forced...)
			Expect(rejected(err)).To(BeTrue(), "unexpected error: %v", err)
			Expect(err.Error()).To(HavePrefix("patch ConfigMap default/" + name + ": "))
			Expect(c.ApplyCalls).To(Equal(1))
		},
		Entry("stale resourceVersion", "applier-precondition-rv", func(ac *corev1ac.ConfigMapApplyConfiguration) {
			ac.WithResourceVersion("1")
		}, apierrors.IsConflict),
		Entry("wrong uid", "applier-precondition-uid", func(ac *corev1ac.ConfigMapApplyConfiguration) {
			ac.WithUID("00000000-0000-0000-0000-000000000000")
		}, apierrors.IsInvalid),
	)

	It("propagates conflicts when alwaysApply requires an API-server check", func() {
		name := "applier-always"
		create(name, map[string]string{"a": "1"})
		c := counting()
		c.OnApply = func(context.Context, runtime.ApplyConfiguration, ...client.ApplyOption) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, name, errors.New("injected conflict"))
		}
		_, err := configMapApplier.PatchApply(testCtx, c, desiredCM(name, map[string]string{"a": "1"}), true, forced...)
		Expect(apierrors.IsConflict(err)).To(BeTrue())
		Expect(c.ApplyCalls).To(Equal(1))
	})

	DescribeTable("applies when ownership cannot be established",
		func(name string, opts []client.ApplyOption, extractFails bool) {
			create(name, map[string]string{"a": "1"})
			applier := configMapApplier
			c := counting()
			if extractFails {
				applier.Extract = func(*corev1.ConfigMap, string) (*corev1ac.ConfigMapApplyConfiguration, error) {
					return nil, errors.New("boom")
				}
			} else {
				c.OnGet = func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := k8sClient.Get(ctx, key, obj, opts...); err != nil {
						return err
					}
					obj.SetManagedFields(nil)
					return nil
				}
			}
			Expect(apply(c, applier, desiredCM(name, map[string]string{"a": "1"}), opts)).
				To(Equal(ssa.PatchApplyResultPatched))
			Expect(c.ApplyCalls).To(Equal(1))
		},
		Entry("managedFields stripped, forced", "applier-no-mf-forced", forced, false),
		Entry("managedFields stripped, unforced", "applier-no-mf-unforced", unforced, false),
		Entry("Extract fails", "applier-extract-error", forced, true),
	)

	It("returns create races as normal API conflicts", func() {
		c := counting()
		c.OnGet = func(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, key.Name)
		}
		c.OnApply = func(context.Context, runtime.ApplyConfiguration, ...client.ApplyOption) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "race", errors.New("injected conflict"))
		}
		_, err := configMapApplier.PatchApply(testCtx, c, desiredCM("race", nil), false, unforced...)
		Expect(apierrors.IsConflict(err)).To(BeTrue())
		Expect(err.Error()).To(HavePrefix("create ConfigMap default/race: "))
	})

	It("classifies a dry-run cache miss without claiming a persisted creation", func() {
		name := "applier-dryrun-missing"
		c := counting()
		result, err := configMapApplier.PatchApply(testCtx, c, desiredCM(name, nil), false,
			client.FieldOwner(owner), client.DryRunAll)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ssa.PatchApplyResultCreated))
		Expect(c.ApplyCalls).To(Equal(1))
		err = k8sClient.Get(testCtx, types.NamespacedName{Name: name, Namespace: testNamespace}, &corev1.ConfigMap{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("classifies a preflight miss even when apply updates a concurrently created object", func() {
		name := "applier-create-race-updated"
		create(name, map[string]string{"a": "1"})
		c := counting()
		c.OnGet = func(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, key.Name)
		}
		result, err := configMapApplier.PatchApply(testCtx, c, desiredCM(name, map[string]string{"a": "2"}), false, forced...)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ssa.PatchApplyResultCreated))
		Expect(getCM(name).Data).To(HaveKeyWithValue("a", "2"))
	})

	It("wraps Get errors other than NotFound", func() {
		c := counting()
		c.OnGet = func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
			return apierrors.NewServiceUnavailable("cache not synced")
		}
		_, err := configMapApplier.PatchApply(testCtx, c, desiredCM("get-error", nil), false, forced...)
		Expect(apierrors.IsServiceUnavailable(err)).To(BeTrue())
		Expect(err.Error()).To(HavePrefix("get ConfigMap default/get-error: "))
		Expect(c.ApplyCalls).To(BeZero())
	})

	It("validates the apply configuration and field owner", func() {
		_, err := configMapApplier.PatchApply(testCtx, k8sClient, nil, false, forced...)
		Expect(err).To(MatchError("configMap ApplyConfiguration must have a name"))
		_, err = configMapApplier.PatchApply(testCtx, k8sClient, corev1ac.ConfigMap("", testNamespace), false, forced...)
		Expect(err).To(MatchError("configMap ApplyConfiguration name must not be empty"))
		_, err = configMapApplier.PatchApply(testCtx, k8sClient, corev1ac.ConfigMap("x", ""), false, forced...)
		Expect(err).To(MatchError("configMap ApplyConfiguration must have a namespace"))
		_, err = configMapApplier.PatchApply(testCtx, k8sClient, corev1ac.ConfigMap("x", testNamespace), false)
		Expect(err).To(MatchError("fieldOwner must not be empty"))
		_, err = configMapApplier.PatchApply(testCtx, k8sClient, corev1ac.ConfigMap("x", testNamespace), false, client.FieldOwner(" "))
		Expect(err).To(MatchError("fieldOwner must not be empty"))
	})

	It("supports cluster-scoped objects", func() {
		namespaces := ssa.Applier[*corev1.Namespace, *corev1ac.NamespaceApplyConfiguration]{
			Kind:    "Namespace",
			New:     func() *corev1.Namespace { return &corev1.Namespace{} },
			Matches: ssa.MatchesJSON[*corev1.Namespace, *corev1ac.NamespaceApplyConfiguration],
			Extract: corev1ac.ExtractNamespace,
		}
		c := counting()
		for _, want := range []ssa.PatchApplyResult{ssa.PatchApplyResultCreated, ssa.PatchApplyResultSkipped} {
			result, err := namespaces.PatchApply(testCtx, c,
				corev1ac.Namespace("applier-cluster-scoped").WithLabels(map[string]string{"team": "a"}), false, forced...)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(want))
		}
		Expect(c.ApplyCalls).To(Equal(1))
	})
})

var _ = Describe("StatusApplier", func() {
	namespaceStatus := ssa.StatusApplier[*corev1.Namespace, *corev1ac.NamespaceApplyConfiguration]{
		Kind:       "Namespace",
		FieldOwner: owner,
		New:        func() *corev1.Namespace { return &corev1.Namespace{} },
		Equal: func(cached, desired *corev1.Namespace) bool {
			reasons := func(ns *corev1.Namespace) map[string]string {
				out := map[string]string{}
				for _, c := range ns.Status.Conditions {
					out[string(c.Type)] = c.Reason
				}
				return out
			}
			return maps.Equal(reasons(cached), reasons(desired))
		},
		ApplyConfiguration: func(ns *corev1.Namespace) *corev1ac.NamespaceApplyConfiguration {
			status := corev1ac.NamespaceStatus()
			for _, condition := range ns.Status.Conditions {
				status.WithConditions(corev1ac.NamespaceCondition().
					WithType(condition.Type).WithStatus(condition.Status).
					WithReason(condition.Reason).WithLastTransitionTime(metav1.Now()))
			}
			return corev1ac.Namespace(ns.Name).WithStatus(status)
		},
	}
	withCondition := func(ns *corev1.Namespace, reason string) *corev1.Namespace {
		ns = ns.DeepCopy()
		ns.Status.Conditions = []corev1.NamespaceCondition{{
			Type: "example.com/Ready", Status: corev1.ConditionTrue, Reason: reason,
		}}
		return ns
	}

	It("applies changed status, skips unchanged status and wraps hook errors", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "status-applier"}}
		Expect(k8sClient.Create(testCtx, ns)).To(Succeed())
		before := 0
		applier := namespaceStatus
		applier.BeforeApply = func(_ context.Context, _ client.Client, cached, desired *corev1.Namespace) error {
			before++
			Expect(cached.Name).To(Equal(desired.Name))
			return nil
		}
		result, err := applier.PatchApply(testCtx, k8sClient, withCondition(ns, "Reconciled"))
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ssa.PatchApplyResultPatched))
		result, err = applier.PatchApply(testCtx, k8sClient, withCondition(ns, "Reconciled"))
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ssa.PatchApplyResultSkipped))
		Expect(before).To(Equal(1))
		hookErr := errors.New("before failed")
		applier.BeforeApply = func(context.Context, client.Client, *corev1.Namespace, *corev1.Namespace) error {
			return hookErr
		}
		_, err = applier.PatchApply(testCtx, k8sClient, withCondition(ns, "Changed"))
		Expect(errors.Is(err, hookErr)).To(BeTrue())
		Expect(err).To(MatchError("before apply Namespace status-applier status: before failed"))
	})

	It("applies unconditionally when the object is not cached", func() {
		_, err := namespaceStatus.PatchApply(testCtx, k8sClient,
			withCondition(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "status-missing"}}, "Reconciled"))
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "unexpected error: %v", err)
		Expect(err.Error()).To(HavePrefix("apply Namespace status-missing status: "))
	})

	It("validates input and wraps Get errors", func() {
		_, err := namespaceStatus.PatchApply(testCtx, k8sClient, nil)
		Expect(err).To(MatchError("namespace must not be nil"))
		_, err = namespaceStatus.PatchApply(testCtx, k8sClient, &corev1.Namespace{})
		Expect(err).To(MatchError("namespace must have a name"))
		c := counting()
		c.OnGet = func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
			return apierrors.NewServiceUnavailable("cache not synced")
		}
		_, err = namespaceStatus.PatchApply(testCtx, c, withCondition(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "status-get-error"},
		}, "Reconciled"))
		Expect(apierrors.IsServiceUnavailable(err)).To(BeTrue())
	})

	It("rejects a nil apply configuration in ApplyStatus", func() {
		var ac *corev1ac.NamespaceApplyConfiguration
		Expect(ssa.ApplyStatus(testCtx, k8sClient, ac, owner)).To(MatchError("applyConfig must not be nil"))
		Expect(ssa.ApplyStatus(testCtx, k8sClient, nil, owner)).To(MatchError("applyConfig must not be nil"))
	})

	DescribeTable("rejects an empty field owner before cache equality or status writes",
		func(fieldOwner string) {
			applier := namespaceStatus
			applier.FieldOwner = fieldOwner
			compared := false
			applier.Equal = func(*corev1.Namespace, *corev1.Namespace) bool {
				compared = true
				return true
			}
			getCalls := 0
			c := counting()
			c.OnGet = func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
				getCalls++
				return nil
			}
			result, err := applier.PatchApply(testCtx, c,
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "status-invalid-owner"}})
			Expect(err).To(MatchError("fieldOwner must not be empty"))
			Expect(result).To(Equal(ssa.PatchApplyResultPatched))
			Expect(getCalls).To(BeZero())
			Expect(compared).To(BeFalse())
			Expect(ssa.ApplyStatus(testCtx, nil,
				corev1ac.Namespace("status-invalid-owner"), fieldOwner)).To(MatchError("fieldOwner must not be empty"))
		},
		Entry("empty", ""),
		Entry("whitespace", " \t"),
	)

	It("wraps standalone status apply errors while preserving their API classification", func() {
		err := ssa.ApplyStatus(testCtx, k8sClient,
			corev1ac.Namespace("standalone-status-missing").WithStatus(corev1ac.NamespaceStatus()), owner)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "unexpected error: %v", err)
		Expect(err.Error()).To(HavePrefix("apply status: "))
	})
})
