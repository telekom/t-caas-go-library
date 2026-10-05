// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package remoteclient

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSecretEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("run make test to provide envtest binaries")
	}
	environment := &envtest.Environment{}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	scheme := runtime.NewScheme()
	requireNoError(t, corev1.AddToScheme(scheme))
	local, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: secretKey.Namespace}}
	requireNoError(t, local.Create(ctx, namespace))
	data, err := clientcmd.Write(clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"envtest": {Server: config.Host, CertificateAuthorityData: config.CAData},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"test": {ClientCertificateData: config.CertData, ClientKeyData: config.KeyData},
		},
		Contexts:       map[string]*clientcmdapi.Context{"envtest": {Cluster: "envtest", AuthInfo: "test", Namespace: namespace.Name}},
		CurrentContext: "envtest",
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: secretKey.Namespace, Name: secretKey.Name},
		Data:       map[string][]byte{"config": data},
	}
	requireNoError(t, local.Create(ctx, secret))
	r, err := New(Options{Scheme: scheme, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Remove(clusterKey) })
	if err := r.RefreshSecret(ctx, clusterKey, local, resolver()); err != nil {
		t.Fatal(err)
	}
	remote, ok := r.Get(clusterKey)
	if !ok {
		t.Fatal("missing remote client")
	}
	if err := remote.Get(ctx, client.ObjectKeyFromObject(secret), &corev1.Secret{}); err != nil {
		t.Fatalf("remote client cannot query envtest server: %v", err)
	}
	before := secret.ResourceVersion
	secret.Data["config"] = []byte("invalid after rotation")
	if err := local.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if secret.ResourceVersion == before {
		t.Fatal("API server did not assign a new resource version")
	}
	// A watch/controller delivers the update to this generic invalidation hook.
	r.InvalidateSecret(client.ObjectKeyFromObject(secret))
	if _, ok := r.Get(clusterKey); ok {
		t.Fatal("Secret update did not invalidate remote client")
	}
	if err := r.RefreshSecret(ctx, clusterKey, local, resolver()); err == nil {
		t.Fatal("refresh used stale Secret data")
	}
	secret.Data["config"] = data
	if err := local.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	r.InvalidateSecret(secretKey)
	if err := r.RefreshSecret(ctx, clusterKey, local, resolver()); err != nil {
		t.Fatal(err)
	}
	if err := local.Delete(ctx, secret); err != nil {
		t.Fatal(err)
	}
	r.InvalidateSecret(secretKey)
	if _, ok := r.Get(clusterKey); ok {
		t.Fatal("deleted Secret left a cached client")
	}
}
