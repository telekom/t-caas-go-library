// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/telekom/t-caas-go-library/pkg/ratelimit"
)

func Example_repeat() {
	// Each independent invocation must start with a fresh token budget.
	main()
	main()
	// Output:
	// allowed: true keys: 1
	// allowed: true keys: 1
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	if err := run(ratelimit.Config{}); err == nil {
		t.Fatal("invalid limiter configuration was accepted")
	}
}
