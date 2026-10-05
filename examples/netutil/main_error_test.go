// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMainWriterFailure(t *testing.T) {
	const helper = "NETUTIL_EXAMPLE_WRITER_FAILURE"
	if os.Getenv(helper) == "1" {
		// A read-only descriptor makes stdout fail without creating files.
		readonly, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout = readonly
		main()
		t.Fatal("main returned despite a failed output write")
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMainWriterFailure$")
	command.Env = append(os.Environ(), helper+"=1")
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "write subnet") {
		t.Fatalf("writer failure: err=%v output=%s", err, output)
	}
}
