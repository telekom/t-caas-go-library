// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dynamiccache "github.com/telekom/t-caas-go-library/pkg/controllerruntime/dynamiccache"
)

// selectedLabelKey is the label key whose per-spec value selects namespaces.
// Each spec uses a unique label VALUE so specs are fully isolated from each
// other even though they share one envtest control plane (namespaces cannot be
// fully deleted in envtest, so leftovers from other specs must never match).
const selectedLabelKey = "test.caas.telekom.com/selected"

// testNamespaceResync keeps the internal namespace informer resync (the retry
// path for failed per-namespace cache construction) fast in tests.
const testNamespaceResync = 1 * time.Second

var (
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client

	nameCounter atomic.Int64
)

func TestDynamicCache(t *testing.T) {
	RegisterFailHandler(Fail)
	// Always randomize spec order (equivalent to ginkgo --randomize-all) so
	// plain `go test` catches inter-spec coupling.
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.RandomizeAllSpecs = true
	RunSpecs(t, "dynamiccache Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	logf.SetLogger(GinkgoLogr)

	SetDefaultEventuallyTimeout(10 * time.Second)
	SetDefaultEventuallyPollingInterval(100 * time.Millisecond)
	SetDefaultConsistentlyDuration(1500 * time.Millisecond)
	SetDefaultConsistentlyPollingInterval(100 * time.Millisecond)

	// KUBEBUILDER_ASSETS is exported by the caller (setup-envtest use -p path);
	// envtest reads it from the environment.
	testEnv = &envtest.Environment{}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: clientgoscheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if testEnv != nil {
		Expect(testEnv.Stop()).To(Succeed())
	}
})

// uniqueName returns a DNS-label-safe name that is unique across specs and
// ginkgo parallel processes (each process runs its own envtest, but stay
// collision-free regardless).
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-p%d-%d", prefix, GinkgoParallelProcess(), nameCounter.Add(1))
}

// newSelector builds the per-spec namespace selector.
func newSelector(value string) labels.Selector {
	return labels.SelectorFromSet(labels.Set{selectedLabelKey: value})
}

// selectedLabels returns namespace labels selecting the namespace for selVal,
// merged with extra labels.
func selectedLabels(selVal string, extra map[string]string) map[string]string {
	lbls := map[string]string{selectedLabelKey: selVal}
	for k, v := range extra {
		lbls[k] = v
	}
	return lbls
}

func createNamespace(name string, lbls map[string]string) {
	GinkgoHelper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls}}
	Expect(k8sClient.Create(context.Background(), ns)).To(Succeed())
}

func createConfigMap(namespace, name string, data map[string]string) {
	GinkgoHelper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data:       data,
	}
	Expect(k8sClient.Create(context.Background(), cm)).To(Succeed())
}

// mutateNamespaceLabels applies mutate to the namespace's labels with
// conflict retries.
func mutateNamespaceLabels(name string, mutate func(map[string]string)) {
	GinkgoHelper()
	Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ns := &corev1.Namespace{}
		if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: name}, ns); err != nil {
			return err
		}
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		mutate(ns.Labels)
		return k8sClient.Update(context.Background(), ns)
	})).To(Succeed())
}

func labelNamespace(name, key, value string) {
	GinkgoHelper()
	mutateNamespaceLabels(name, func(lbls map[string]string) { lbls[key] = value })
}

func unlabelNamespace(name string) {
	GinkgoHelper()
	mutateNamespaceLabels(name, func(lbls map[string]string) { delete(lbls, selectedLabelKey) })
}

// newUnstartedCache constructs a dynamic cache for the per-spec selector value
// without starting it (needed to register field indexes before Start).
func newUnstartedCache(selVal string) cache.Cache {
	GinkgoHelper()
	dc, err := dynamiccache.New(cfg, cache.Options{Scheme: clientgoscheme.Scheme}, dynamiccache.Options{
		NamespaceSelector: newSelector(selVal),
		NamespaceResync:   testNamespaceResync,
	})
	Expect(err).NotTo(HaveOccurred())
	return dc
}

// startCache runs the cache in a goroutine bound to a per-spec context
// (cancelled via DeferCleanup) and waits for the initial sync.
func startCache(dc cache.Cache) {
	GinkgoHelper()
	runCtx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)
	go func() {
		defer GinkgoRecover()
		Expect(dc.Start(runCtx)).To(Succeed())
	}()
	syncCtx, syncCancel := context.WithTimeout(runCtx, 10*time.Second)
	defer syncCancel()
	Expect(dc.WaitForCacheSync(syncCtx)).To(BeTrue())
}

func newStartedCache(selVal string) cache.Cache {
	GinkgoHelper()
	dc := newUnstartedCache(selVal)
	startCache(dc)
	return dc
}

// eventRecorder is a thread-safe recording toolscache.ResourceEventHandler.
type eventRecorder struct {
	mu      sync.Mutex
	adds    []client.Object
	updates []client.Object
	deletes []any
}

var _ toolscache.ResourceEventHandler = &eventRecorder{}

func newEventRecorder() *eventRecorder { return &eventRecorder{} }

func (r *eventRecorder) OnAdd(obj any, _ bool) {
	if o, ok := obj.(client.Object); ok {
		r.mu.Lock()
		r.adds = append(r.adds, o)
		r.mu.Unlock()
	}
}

func (r *eventRecorder) OnUpdate(_, newObj any) {
	if o, ok := newObj.(client.Object); ok {
		r.mu.Lock()
		r.updates = append(r.updates, o)
		r.mu.Unlock()
	}
}

func (r *eventRecorder) OnDelete(obj any) {
	r.mu.Lock()
	r.deletes = append(r.deletes, obj)
	r.mu.Unlock()
}

func objKey(o client.Object) string {
	if o.GetNamespace() == "" {
		return o.GetName()
	}
	return o.GetNamespace() + "/" + o.GetName()
}

func (r *eventRecorder) addKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.adds))
	for _, o := range r.adds {
		keys = append(keys, objKey(o))
	}
	return keys
}

func (r *eventRecorder) addCount(key string) int {
	n := 0
	for _, k := range r.addKeys() {
		if k == key {
			n++
		}
	}
	return n
}

func (r *eventRecorder) updateObjects() []client.Object {
	r.mu.Lock()
	defer r.mu.Unlock()
	objs := make([]client.Object, len(r.updates))
	copy(objs, r.updates)
	return objs
}

// tombstones returns the recorded DeletedFinalStateUnknown delete events.
func (r *eventRecorder) tombstones() []toolscache.DeletedFinalStateUnknown {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []toolscache.DeletedFinalStateUnknown
	for _, d := range r.deletes {
		if t, ok := d.(toolscache.DeletedFinalStateUnknown); ok {
			out = append(out, t)
		}
	}
	return out
}

func (r *eventRecorder) tombstoneKeys() []string {
	ts := r.tombstones()
	keys := make([]string, 0, len(ts))
	for _, t := range ts {
		keys = append(keys, t.Key)
	}
	return keys
}

func (r *eventRecorder) tombstoneFor(key string) (toolscache.DeletedFinalStateUnknown, bool) {
	for _, t := range r.tombstones() {
		if t.Key == key {
			return t, true
		}
	}
	return toolscache.DeletedFinalStateUnknown{}, false
}
