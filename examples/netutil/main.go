// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command netutil demonstrates bounded subnet subdivision.
package main

import (
	"fmt"
	"io"
	"net/netip"
	"os"

	"github.com/telekom/t-caas-go-library/pkg/netutil"
)

func run(out io.Writer, prefix netip.Prefix) error {
	subnets, err := netutil.Subdivide(prefix, 26, 4)
	if err != nil {
		return fmt.Errorf("subdivide documentation network: %w", err)
	}
	for _, subnet := range subnets {
		first, err := netutil.FirstUsable(subnet)
		if err != nil {
			return fmt.Errorf("first usable address: %w", err)
		}
		if _, err := fmt.Fprintf(out, "%s: first usable %s\n", subnet, first); err != nil {
			return fmt.Errorf("write subnet: %w", err)
		}
	}
	return nil
}

func main() {
	if err := run(os.Stdout, netip.MustParsePrefix("192.0.2.0/24")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
