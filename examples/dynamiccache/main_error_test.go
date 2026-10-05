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

func TestMainReportsEmptySelector(t *testing.T) {
	const helper = "DYNAMICCACHE_EXAMPLE_EMPTY_SELECTOR"
	if os.Getenv(helper) == "1" {
		os.Args = []string{"dynamiccache-example", "--selector="}
		main()
		t.Fatal("main returned despite an empty selector")
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMainReportsEmptySelector$")
	command.Env = append(os.Environ(), helper+"=1")
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "--selector must not be empty") {
		t.Fatalf("empty selector: err=%v output=%s", err, output)
	}
}
