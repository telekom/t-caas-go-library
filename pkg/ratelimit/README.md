<!-- SPDX-FileCopyrightText: 2026 Deutsche Telekom AG -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Keyed rate-limiter adapter

This package adds bounded per-key lookup, LRU eviction and idle expiry around
`golang.org/x/time/rate`; upstream owns token accounting. The storage glue is
shared by auth-operator and k8s-breakglass, whose authenticated key selection,
global limits and response handling remain local.

## Use upstream instead for…

| Need | Upstream import path / API |
|------|----------------------------|
| Single/global token bucket | `golang.org/x/time/rate`: `NewLimiter`, `Allow`, `Wait` |
| Generic retry | `github.com/cenkalti/backoff/v5`: `Retry`, `WithMaxTries`, `WithMaxElapsedTime`, `WithNotify`, `Permanent` |
| Kubernetes polling/backoff | `k8s.io/apimachinery/pkg/util/wait`: `ExponentialBackoffWithContext`, `PollUntilContextTimeout` |
| Circuit breaking | `github.com/sony/gobreaker/v2`: `CircuitBreaker.Execute`, `TwoStepCircuitBreaker.Allow`; configure thresholds and classification explicitly |
| Coalescing/debounce and controller retries | `k8s.io/client-go/util/workqueue`: typed delaying/rate-limiting queues, `AddAfter`, `Get`/`Done`, `AddRateLimited`, `Forget` |

Do not recreate the dropped retry, breaker or debounce engines even while
consumer adoption is pending. See the [upstream guide](../../docs/upstream-libraries.md)
and [source-specific migration notes](../../README.md#recommended-resilience-upstream-libraries).

Eviction resets budgets: use trusted keys and an independent global limiter.
Choose an idle TTL at least as long as full-bucket refill when expiration must
not accelerate replenishment. Rates must be in `(0, 1e9]` tokens/second to respect
the upstream limiter's nanosecond resolution.
