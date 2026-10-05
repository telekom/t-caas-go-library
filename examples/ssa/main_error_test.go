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

func TestMainReportsInvalidFlags(t *testing.T) {
	const helper = "SSA_EXAMPLE_INVALID_FLAGS"
	if os.Getenv(helper) == "1" {
		os.Args = []string{"ssa-example", "--data=invalid"}
		main()
		t.Fatal("main returned despite invalid flags")
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMainReportsInvalidFlags$")
	command.Env = append(os.Environ(), helper+"=1")
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "expected key=value") {
		t.Fatalf("invalid flags: err=%v output=%s", err, output)
	}
}
