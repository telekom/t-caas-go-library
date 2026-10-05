// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package e2e contains an integration suite that verifies the dynamiccache
// contract against a real cluster with auth-operator
// (https://github.com/telekom/auth-operator) installed.
//
// The suite is guarded: it is skipped unless the environment variable E2E=true
// is set. It connects to the cluster addressed by KUBECONFIG. The admin
// kubeconfig is used to provision the test fixtures (ServiceAccount,
// ClusterRoles, BindDefinition, namespaces, ConfigMaps); the dynamiccache under
// test runs with a restricted rest.Config minted for the ServiceAccount via a
// TokenRequest, so it only ever holds the RBAC that auth-operator grants it.
package e2e

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
)

const (
	// fixtureNamespace hosts the ServiceAccount whose token drives the cache.
	fixtureNamespace = "dynamiccache-e2e"
	// serviceAccountName is the restricted identity the cache runs as.
	serviceAccountName = "dynamiccache-e2e"
	// selectionLabel marks namespaces that the dynamiccache (and the
	// auth-operator BindDefinition) select. auth-operator's validating
	// webhook only admits tracked ownership label keys in BindDefinition
	// namespace selectors (t-caas.telekom.com/{owner,tenant,thirdparty} or
	// kubernetes.io/metadata.name), so the selection must use one of them;
	// uniqueness comes from the value.
	selectionLabel = "t-caas.telekom.com/thirdparty"
	// selectionValue is the label value marking this suite's namespaces.
	selectionValue = "dynamiccache-e2e"
	// configMapReaderClusterRole grants get/list/watch on ConfigMaps; it is
	// bound per selected namespace by auth-operator via the BindDefinition.
	configMapReaderClusterRole = "dynamiccache-e2e-configmap-reader"
	// namespaceWatcherClusterRole grants get/list/watch on Namespaces; it is
	// bound cluster-wide directly (the namespace informer is the only
	// cluster-scoped permission dynamiccache needs).
	namespaceWatcherClusterRole = "dynamiccache-e2e-namespace-watcher"
	// bindDefinitionName is the auth-operator BindDefinition that provisions
	// the per-namespace RoleBindings asynchronously.
	bindDefinitionName = "dynamiccache-e2e"

	// convergenceTimeout is deliberately generous: auth-operator creates the
	// per-namespace RoleBinding asynchronously after a namespace is labeled,
	// and the per-namespace informer retries its initial LIST with backoff
	// until the grant lands.
	convergenceTimeout = 2 * time.Minute
	pollInterval       = 2 * time.Second
)

var (
	adminCfg      *rest.Config
	adminClient   client.Client
	restrictedCfg *rest.Config
	testScheme    *runtime.Scheme

	dynCache    cache.Cache
	cacheCancel context.CancelFunc
	cmEvents    *eventRecorder
)

func TestE2E(t *testing.T) {
	if os.Getenv("E2E") != "true" {
		t.Skip("set E2E=true to run the dynamiccache auth-operator e2e suite")
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "dynamiccache auth-operator e2e suite")
}

var _ = BeforeSuite(func(ctx SpecContext) {
	var err error
	adminCfg, err = config.GetConfig()
	Expect(err).NotTo(HaveOccurred(), "loading admin kubeconfig (KUBECONFIG)")

	testScheme = runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(testScheme)).To(Succeed())

	adminClient, err = client.New(adminCfg, client.Options{Scheme: testScheme})
	Expect(err).NotTo(HaveOccurred())

	createFixtures(ctx)

	restrictedCfg = restrictedConfigFor(ctx, fixtureNamespace, serviceAccountName)

	// The cache under test uses ONLY the restricted ServiceAccount config.
	dynCache, err = dynamiccache.New(restrictedCfg, cache.Options{Scheme: testScheme}, dynamiccache.Options{
		NamespaceSelector: labels.SelectorFromSet(labels.Set{selectionLabel: selectionValue}),
	})
	Expect(err).NotTo(HaveOccurred())

	cmEvents = &eventRecorder{}
	informer, err := dynCache.GetInformer(ctx, &corev1.ConfigMap{})
	Expect(err).NotTo(HaveOccurred())
	_, err = informer.AddEventHandler(cmEvents)
	Expect(err).NotTo(HaveOccurred())

	var cacheCtx context.Context
	cacheCtx, cacheCancel = context.WithCancel(context.Background())
	go func() {
		defer GinkgoRecover()
		Expect(dynCache.Start(cacheCtx)).To(Succeed())
	}()

	syncCtx, syncCancel := context.WithTimeout(ctx, time.Minute)
	defer syncCancel()
	Expect(dynCache.WaitForCacheSync(syncCtx)).To(BeTrue(), "initial cache sync")
})

var _ = AfterSuite(func(ctx SpecContext) {
	if cacheCancel != nil {
		cacheCancel()
	}
	if adminClient == nil {
		return
	}
	for _, obj := range fixtureObjects() {
		err := adminClient.Delete(ctx, obj)
		if err != nil && !apierrors.IsNotFound(err) {
			GinkgoWriter.Printf("cleanup of %T %q failed: %v\n", obj, obj.GetName(), err)
		}
	}
})

// createFixtures provisions, with the admin client, everything the restricted
// ServiceAccount needs. Creation is idempotent so the suite can be re-run
// against the same cluster.
func createFixtures(ctx context.Context) {
	for _, obj := range fixtureObjects() {
		err := adminClient.Create(ctx, obj)
		if apierrors.IsAlreadyExists(err) {
			continue
		}
		Expect(err).NotTo(HaveOccurred(), "creating fixture %T %q", obj, obj.GetName())
	}
}

func fixtureObjects() []client.Object {
	return []client.Object{
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: fixtureNamespace},
		},
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: fixtureNamespace},
		},
		// Bound per selected namespace by auth-operator (see the
		// BindDefinition below).
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: configMapReaderClusterRole},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "list", "watch"},
			}},
		},
		// The namespace informer is the single cluster-scoped watch the
		// dynamiccache requires; grant it directly.
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: namespaceWatcherClusterRole},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"namespaces"},
				Verbs:     []string{"get", "list", "watch"},
			}},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: namespaceWatcherClusterRole},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     namespaceWatcherClusterRole,
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      serviceAccountName,
				Namespace: fixtureNamespace,
			}},
		},
		// auth-operator BindDefinition (authorization.t-caas.telekom.com/v1alpha1):
		// creates a RoleBinding to configMapReaderClusterRole in every
		// namespace matching spec.roleBindings[].namespaceSelector (a list of
		// metav1.LabelSelector), asynchronously as namespaces are (un)labeled.
		bindDefinition(),
	}
}

func bindDefinition() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("authorization.t-caas.telekom.com/v1alpha1")
	u.SetKind("BindDefinition")
	u.SetName(bindDefinitionName)
	u.Object["spec"] = map[string]any{
		"targetName": bindDefinitionName,
		"subjects": []any{
			map[string]any{
				"kind":      "ServiceAccount",
				"name":      serviceAccountName,
				"namespace": fixtureNamespace,
			},
		},
		"roleBindings": []any{
			map[string]any{
				"clusterRoleRefs": []any{configMapReaderClusterRole},
				"namespaceSelector": []any{
					map[string]any{
						"matchLabels": map[string]any{selectionLabel: selectionValue},
					},
				},
			},
		},
	}
	return u
}

// restrictedConfigFor mints a token for the ServiceAccount via the
// TokenRequest API (the client-go equivalent of `kubectl create token`) and
// returns a rest.Config that authenticates only with that token.
func restrictedConfigFor(ctx context.Context, namespace, name string) *rest.Config {
	clientset, err := kubernetes.NewForConfig(adminCfg)
	Expect(err).NotTo(HaveOccurred())

	tokenRequest, err := clientset.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, name,
		&authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: ptr.To(int64((6 * time.Hour).Seconds())),
			},
		}, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "minting ServiceAccount token")
	Expect(tokenRequest.Status.Token).NotTo(BeEmpty())

	cfg := rest.AnonymousClientConfig(adminCfg)
	cfg.BearerToken = tokenRequest.Status.Token
	return cfg
}

// eventRecorder is a thread-safe toolscache.ResourceEventHandler that records
// add and delete event keys (namespace/name), including synthetic deletes
// delivered as cache.DeletedFinalStateUnknown.
type eventRecorder struct {
	mu      sync.Mutex
	adds    []string
	deletes []string
}

var _ toolscache.ResourceEventHandler = (*eventRecorder)(nil)

func (r *eventRecorder) OnAdd(obj any, _ bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adds = append(r.adds, eventKey(obj))
}

func (r *eventRecorder) OnUpdate(any, any) {}

func (r *eventRecorder) OnDelete(obj any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletes = append(r.deletes, eventKey(obj))
}

// AddsIn returns the recorded add keys within the given namespace.
func (r *eventRecorder) AddsIn(namespace string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return filterByNamespace(r.adds, namespace)
}

// DeletesIn returns the recorded delete keys within the given namespace.
func (r *eventRecorder) DeletesIn(namespace string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return filterByNamespace(r.deletes, namespace)
}

// Reset clears all recorded events.
func (r *eventRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adds = nil
	r.deletes = nil
}

func eventKey(obj any) string {
	if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		return tombstone.Key
	}
	if key, err := toolscache.MetaNamespaceKeyFunc(obj); err == nil {
		return key
	}
	return fmt.Sprintf("<unknown:%T>", obj)
}

func filterByNamespace(keys []string, namespace string) []string {
	prefix := namespace + "/"
	var out []string
	for _, k := range keys {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out
}
