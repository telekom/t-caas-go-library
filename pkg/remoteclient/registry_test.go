// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package remoteclient

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var clusterKey = types.NamespacedName{Namespace: "clusters", Name: "remote"}
var secretKey = types.NamespacedName{Namespace: "credentials", Name: "remote"}

func credentials(t *testing.T, token string) []byte {
	t.Helper()
	data, err := clientcmd.Write(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"remote": {Server: "https://cluster.example"}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"operator": {Token: token}},
		Contexts:       map[string]*clientcmdapi.Context{"remote": {Cluster: "remote", AuthInfo: "operator"}},
		CurrentContext: "remote",
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func registry(t *testing.T, options Options) *Registry {
	t.Helper()
	if options.Factory == nil {
		options.Factory = func(context.Context, *rest.Config, *http.Client) (client.Client, error) {
			return fake.NewClientBuilder().Build(), nil
		}
	}
	r, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Remove(clusterKey) })
	return r
}

func TestTunablesAndCopies(t *testing.T) {
	for _, options := range []Options{
		{QPS: -1}, {QPS: float32(math.NaN())}, {QPS: float32(math.Inf(1))}, {QPS: float32(math.Inf(-1))},
		{Burst: -1}, {Timeout: -time.Second},
	} {
		if _, err := New(options); err == nil {
			t.Fatal("invalid option accepted")
		} else if err.Error() != "QPS must be finite and non-negative; burst and timeout must be non-negative" {
			t.Fatalf("misleading validation error: %v", err)
		}
	}
	r := registry(t, Options{QPS: 10, Burst: 20})
	if _, ok := r.Get(clusterKey); ok {
		t.Fatal("unexpected client")
	}
	if _, ok := r.Config(clusterKey); ok {
		t.Fatal("unexpected config")
	}
	if err := r.Refresh(context.Background(), types.NamespacedName{}, credentials(t, "token")); err == nil {
		t.Fatal("invalid key accepted")
	}
	if err := r.Refresh(context.Background(), clusterKey, credentials(t, "token")); err != nil {
		t.Fatal(err)
	}
	cfg, ok := r.Config(clusterKey)
	if !ok || cfg.QPS != 10 || cfg.Burst != 20 || cfg.Timeout != 0 {
		t.Fatal("tunables not preserved")
	}
	cfg.BearerToken = "mutated"
	next, _ := r.Config(clusterKey)
	if next.BearerToken != "token" {
		t.Fatal("returned config aliases registry state")
	}
	original := &rest.Config{
		TLSClientConfig: rest.TLSClientConfig{CAData: []byte("ca"), CertData: []byte("cert"), KeyData: []byte("key")},
		Impersonate:     rest.ImpersonationConfig{Groups: []string{"group"}, Extra: map[string][]string{"extra": {"value"}}},
	}
	copied := copyConfig(original)
	copied.CAData[0], copied.CertData[0], copied.KeyData[0] = 'x', 'x', 'x'
	if string(original.CAData) != "ca" || string(original.CertData) != "cert" || string(original.KeyData) != "key" {
		t.Fatal("TLS data aliases config")
	}
	copied.Impersonate.Groups[0], copied.Impersonate.Extra["extra"][0] = "changed", "changed"
	if original.Impersonate.Groups[0] != "group" || original.Impersonate.Extra["extra"][0] != "value" {
		t.Fatal("impersonation config aliases original")
	}
}

func TestReplacementClosesTransport(t *testing.T) {
	var transports []*closingTransport
	r := registry(t, Options{WrapTransport: func(base http.RoundTripper) http.RoundTripper {
		tr := &closingTransport{RoundTripper: base}
		transports = append(transports, tr)
		return tr
	}})
	for _, token := range []string{"old", "new"} {
		if err := r.Refresh(context.Background(), clusterKey, credentials(t, token)); err != nil {
			t.Fatal(err)
		}
	}
	if transports[0].closed.Load() == 0 || transports[1].closed.Load() != 0 {
		t.Fatal("replacement did not close only the old transport")
	}
}

func TestFailedNewRefreshFencesOld(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	r := registry(t, Options{Factory: func(context.Context, *rest.Config, *http.Client) (client.Client, error) {
		close(started)
		<-release
		return fake.NewClientBuilder().Build(), nil
	}})
	data := credentials(t, "old")
	done := make(chan error, 1)
	go func() { done <- r.Refresh(context.Background(), clusterKey, data) }()
	<-started
	if err := r.Refresh(context.Background(), clusterKey, []byte("invalid")); err == nil {
		t.Fatal("invalid credentials accepted")
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrSuperseded) {
		t.Fatalf("old refresh not fenced by failed new refresh: %v", err)
	}
}

type closingTransport struct {
	http.RoundTripper
	closed atomic.Int32
}

func (t *closingTransport) CloseIdleConnections() { t.closed.Add(1) }

func TestRefreshOrderingAndCleanup(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprint(remove), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			var transports []*closingTransport
			r := registry(t, Options{
				WrapTransport: func(base http.RoundTripper) http.RoundTripper {
					tr := &closingTransport{RoundTripper: base}
					mu.Lock()
					transports = append(transports, tr)
					mu.Unlock()
					return tr
				},
				Factory: func(_ context.Context, cfg *rest.Config, _ *http.Client) (client.Client, error) {
					if cfg.BearerToken == "old" {
						close(started)
						<-release
					}
					return fake.NewClientBuilder().Build(), nil
				},
			})
			old := credentials(t, "old")
			done := make(chan error, 1)
			go func() { done <- r.Refresh(context.Background(), clusterKey, old) }()
			<-started
			if remove {
				r.Remove(clusterKey)
			} else if err := r.Refresh(context.Background(), clusterKey, credentials(t, "new")); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-done; !errors.Is(err, ErrSuperseded) {
				t.Fatalf("expected superseded refresh: %v", err)
			}
			cfg, ok := r.Config(clusterKey)
			if remove && ok || !remove && (!ok || cfg.BearerToken != "new") {
				t.Fatal("stale refresh resurrected or overwrote client")
			}
			r.Remove(clusterKey)
			mu.Lock()
			defer mu.Unlock()
			for _, transport := range transports {
				if transport.closed.Load() == 0 {
					t.Fatal("idle connections not closed")
				}
			}
		})
	}
}

func TestFailures(t *testing.T) {
	r := registry(t, Options{})
	ctx := context.Background()
	if err := r.Refresh(ctx, clusterKey, credentials(t, "original")); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{[]byte("token: [very-secret"), []byte(""), []byte("clusters: wrong")} {
		err := r.Refresh(ctx, clusterKey, data)
		if !errors.Is(err, ErrInvalidKubeconfig) || strings.Contains(err.Error(), "very-secret") {
			t.Fatalf("parse error not sanitized: %v", err)
		}
	}
	raw, _ := clientcmd.Load(credentials(t, "token"))
	for _, mutate := range []func(){
		func() { raw.Clusters["remote"].CertificateAuthority = "local-ca" },
		func() { raw.AuthInfos["operator"].TokenFile = "local-token" },
	} {
		mutate()
		data, _ := clientcmd.Write(*raw)
		if err := r.Refresh(ctx, clusterKey, data); !errors.Is(err, ErrInvalidKubeconfig) {
			t.Fatal("local file references accepted")
		}
		raw.Clusters["remote"].CertificateAuthority = ""
	}
	cfg, _ := r.Config(clusterKey)
	if cfg.BearerToken != "original" {
		t.Fatal("failure evicted original")
	}
	failure := errors.New("factory failure")
	for _, factory := range []Factory{
		func(context.Context, *rest.Config, *http.Client) (client.Client, error) { return nil, failure },
		func(context.Context, *rest.Config, *http.Client) (client.Client, error) {
			return nil, nil //nolint:nilnil // Exercise a malformed factory result.
		},
	} {
		other := registry(t, Options{Factory: factory})
		if err := other.Refresh(ctx, clusterKey, credentials(t, "token")); err == nil {
			t.Fatal("factory failure accepted")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := r.Refresh(canceled, clusterKey, credentials(t, "token")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	normal, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := normal.Refresh(canceled, clusterKey, credentials(t, "token")); !errors.Is(err, context.Canceled) {
		t.Fatalf("default factory cancellation ignored: %v", err)
	}
}

func resolver() Resolver {
	return ResolverFunc(func(context.Context, types.NamespacedName) (SecretReference, error) {
		return SecretReference{Secret: secretKey, DataKey: "config"}, nil
	})
}

func secretReader(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: secretKey.Namespace, Name: secretKey.Name},
		Data:       map[string][]byte{"config": credentials(t, "secret-token")},
	}).Build()
}

func TestSecretTracking(t *testing.T) {
	var calls atomic.Int32
	var r *Registry
	r = registry(t, Options{OnInvalidate: func(key types.NamespacedName) {
		r.Get(key) // Hooks must be reentrant.
		calls.Add(1)
	}})
	reader := secretReader(t)
	ctx := context.Background()
	if err := r.RefreshSecret(ctx, clusterKey, reader, resolver()); err != nil {
		t.Fatal(err)
	}
	other := types.NamespacedName{Namespace: "clusters", Name: "other"}
	if err := r.RefreshSecret(ctx, other, reader, resolver()); err != nil {
		t.Fatal(err)
	}
	r.InvalidateSecret(types.NamespacedName{Name: "unrelated"})
	if _, ok := r.Get(clusterKey); !ok {
		t.Fatal("unrelated invalidation evicted client")
	}
	// Failed ref change must retain the old client's dependency.
	failed := ResolverFunc(func(context.Context, types.NamespacedName) (SecretReference, error) {
		return SecretReference{Secret: types.NamespacedName{Namespace: "missing", Name: "missing"}, DataKey: "config"}, nil
	})
	if err := r.RefreshSecret(ctx, clusterKey, reader, failed); err == nil {
		t.Fatal("missing Secret accepted")
	}
	r.InvalidateSecret(secretKey)
	if _, ok := r.Get(clusterKey); ok {
		t.Fatal("old dependency lost on failed refresh")
	}
	if _, ok := r.Get(other); ok || calls.Load() != 2 {
		t.Fatal("shared Secret invalidation failed")
	}
	if err := r.RefreshSecret(ctx, clusterKey, reader, resolver()); err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(ctx, clusterKey, credentials(t, "direct")); err != nil {
		t.Fatal(err)
	}
	r.InvalidateSecret(secretKey)
	if _, ok := r.Get(clusterKey); !ok {
		t.Fatal("direct replacement kept old dependency")
	}
}

func TestSecretErrorsAndRaces(t *testing.T) {
	reader := secretReader(t)
	r := registry(t, Options{})
	ctx := context.Background()
	for _, res := range []Resolver{
		nil,
		ResolverFunc(func(context.Context, types.NamespacedName) (SecretReference, error) {
			return SecretReference{}, errors.New("reference failure")
		}),
		ResolverFunc(func(context.Context, types.NamespacedName) (SecretReference, error) { return SecretReference{}, nil }),
		ResolverFunc(func(context.Context, types.NamespacedName) (SecretReference, error) {
			return SecretReference{Secret: secretKey, DataKey: "missing"}, nil
		}),
	} {
		if err := r.RefreshSecret(ctx, clusterKey, reader, res); err == nil {
			t.Fatal("invalid reference accepted")
		}
	}
	if err := r.RefreshSecret(ctx, types.NamespacedName{}, reader, resolver()); err == nil {
		t.Fatal("invalid key accepted")
	}
	started, release := make(chan struct{}), make(chan struct{})
	blocked := ResolverFunc(func(context.Context, types.NamespacedName) (SecretReference, error) {
		close(started)
		<-release
		return SecretReference{Secret: secretKey, DataKey: "config"}, nil
	})
	done := make(chan error, 1)
	go func() { done <- r.RefreshSecret(ctx, clusterKey, reader, blocked) }()
	<-started
	r.Remove(clusterKey)
	close(release)
	if err := <-done; !errors.Is(err, ErrSuperseded) {
		t.Fatalf("resolver race not fenced: %v", err)
	}
	// Invalidate after dependency registration but before construction completes.
	started, release = make(chan struct{}), make(chan struct{})
	r = registry(t, Options{Factory: func(context.Context, *rest.Config, *http.Client) (client.Client, error) {
		close(started)
		<-release
		return fake.NewClientBuilder().Build(), nil
	}})
	go func() { done <- r.RefreshSecret(ctx, clusterKey, reader, resolver()) }()
	<-started
	r.InvalidateSecret(secretKey)
	close(release)
	if err := <-done; !errors.Is(err, ErrSuperseded) {
		t.Fatalf("Secret race not fenced: %v", err)
	}
}

func TestConcurrentOperations(t *testing.T) {
	r := registry(t, Options{})
	data := credentials(t, "token")
	reader := secretReader(t)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			for range 10 {
				_ = r.Refresh(context.Background(), clusterKey, data)
				_ = r.RefreshSecret(context.Background(), clusterKey, reader, resolver())
				r.Get(clusterKey)
				r.Config(clusterKey)
				r.InvalidateSecret(secretKey)
				r.Remove(clusterKey)
			}
		})
	}
	wg.Wait()
}

func ExampleRegistry() {
	r, err := New(Options{Factory: func(context.Context, *rest.Config, *http.Client) (client.Client, error) {
		return fake.NewClientBuilder().Build(), nil
	}})
	if err != nil {
		panic(err)
	}
	key := types.NamespacedName{Namespace: "clusters", Name: "remote"}
	data, err := clientcmd.Write(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"remote": {Server: "https://cluster.example"}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"operator": {Token: "example-token"}},
		Contexts:       map[string]*clientcmdapi.Context{"remote": {Cluster: "remote", AuthInfo: "operator"}},
		CurrentContext: "remote",
	})
	if err != nil {
		panic(err)
	}
	if err := r.Refresh(context.Background(), key, data); err != nil {
		panic(err)
	}
	_, exists := r.Get(key)
	fmt.Println(exists)
	r.Remove(key)
	_, exists = r.Get(key)
	fmt.Println(exists)
	// Output:
	// true
	// false
}
