// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package dynamiccache

import (
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// NewWithCacheFunc exposes the per-namespace cache constructor hook to the
// external test package for failure injection.
func NewWithCacheFunc(config *rest.Config, cacheOpts cache.Options, opts Options, newCache cache.NewCacheFunc) (cache.Cache, error) {
	opts.newCache = newCache
	return New(config, cacheOpts, opts)
}
