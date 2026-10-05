// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestMainReportsMissingCertificateOptions(t *testing.T) {
	const helper = "CERTROTATION_EXAMPLE_MISSING_OPTIONS"
	if os.Getenv(helper) == "1" {
		os.Args = []string{"certrotation-example", "--health-probe-bind-address=0"}
		main()
		t.Fatal("main returned despite missing certificate options")
	}
	configPath := filepath.Join(t.TempDir(), "kubeconfig")
	cfg := clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"demo": {Server: "https://127.0.0.1:1"}},
		Contexts:       map[string]*clientcmdapi.Context{"demo": {Cluster: "demo"}},
		CurrentContext: "demo",
	}
	if err := clientcmd.WriteToFile(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMainReportsMissingCertificateOptions$")
	command.Env = append(os.Environ(), helper+"=1", "KUBECONFIG="+configPath)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "certificate rotation") {
		t.Fatalf("missing certificate options: err=%v output=%s", err, output)
	}
}
