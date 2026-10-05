// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command ratelimit demonstrates bounded keyed token buckets.
package main

import (
	"fmt"
	"time"

	"github.com/telekom/t-caas-go-library/pkg/ratelimit"
)

func run(config ratelimit.Config) error {
	l, err := ratelimit.New(config)
	if err != nil {
		return err
	}
	allowed, _ := l.Allow("authenticated-user")
	fmt.Println("allowed:", allowed, "keys:", l.Len())
	return nil
}

func main() {
	if err := run(ratelimit.Config{Rate: 1, Burst: 1, MaxKeys: 100, IdleTTL: time.Minute}); err != nil {
		panic(err)
	}
}
