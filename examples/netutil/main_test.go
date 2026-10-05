// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out, netip.MustParsePrefix("192.0.2.0/24")); err != nil {
		t.Fatal(err)
	}
	want := "192.0.2.0/26: first usable 192.0.2.1\n" +
		"192.0.2.64/26: first usable 192.0.2.65\n" +
		"192.0.2.128/26: first usable 192.0.2.129\n" +
		"192.0.2.192/26: first usable 192.0.2.193\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	if err := run(failingWriter{}, netip.MustParsePrefix("192.0.2.0/24")); err == nil || !strings.Contains(err.Error(), "write subnet") {
		t.Fatalf("expected writer error, got %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic write failure")
}

func Example() {
	main()
	// Output:
	// 192.0.2.0/26: first usable 192.0.2.1
	// 192.0.2.64/26: first usable 192.0.2.65
	// 192.0.2.128/26: first usable 192.0.2.129
	// 192.0.2.192/26: first usable 192.0.2.193
}

func TestRunRejectsInvalidPrefix(t *testing.T) {
	if err := run(&bytes.Buffer{}, netip.Prefix{}); err == nil {
		t.Fatal("invalid prefix was accepted")
	}
}
