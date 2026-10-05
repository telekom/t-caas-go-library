// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command remoteclient demonstrates Secret loading and invalidation without a cluster.
package main

import (
	"context"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/telekom/t-caas-go-library/pkg/remoteclient"
)

func run(ctx context.Context, options remoteclient.Options) error {
	const clusterName = "remote"
	data, err := clientcmd.Write(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{clusterName: {Server: "https://cluster.example"}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"operator": {Token: "example-token"}},
		Contexts:       map[string]*clientcmdapi.Context{clusterName: {Cluster: clusterName, AuthInfo: "operator"}},
		CurrentContext: clusterName,
	})
	if err != nil {
		return fmt.Errorf("build kubeconfig: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "credentials", Name: clusterName},
		Data:       map[string][]byte{"kubeconfig": data},
	}
	reader := fake.NewClientBuilder().WithObjects(secret).Build()
	registry, err := remoteclient.New(options)
	if err != nil {
		return fmt.Errorf("create registry: %w", err)
	}
	cluster := types.NamespacedName{Namespace: "clusters", Name: clusterName}
	defer registry.Remove(cluster)
	resolver := remoteclient.ResolverFunc(func(context.Context, types.NamespacedName) (remoteclient.SecretReference, error) {
		return remoteclient.SecretReference{Secret: client.ObjectKeyFromObject(secret), DataKey: "kubeconfig"}, nil
	})
	if err := registry.RefreshSecret(ctx, cluster, reader, resolver); err != nil {
		return fmt.Errorf("load remote credentials: %w", err)
	}
	_, present := registry.Get(cluster)
	if !present {
		return fmt.Errorf("remote client missing after refresh")
	}
	registry.InvalidateSecret(client.ObjectKeyFromObject(secret))
	_, present = registry.Get(cluster)
	if present {
		return fmt.Errorf("remote client remained after invalidation")
	}
	fmt.Println("Secret-backed client loaded and invalidated")
	return nil
}

func offlineClient(ctx context.Context, _ *rest.Config, _ *http.Client) (client.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return fake.NewClientBuilder().Build(), nil
}

func main() {
	// Production callers omit Factory to construct a real controller-runtime client.
	if err := run(context.Background(), remoteclient.Options{Factory: offlineClient}); err != nil {
		panic(err)
	}
}
