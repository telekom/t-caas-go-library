// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"testing"

	"github.com/telekom/t-caas-go-library/pkg/redfish/redfishtest"
)

func TestRun(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{})
	if err := run(t.Context(), server.URL); err != nil {
		t.Fatal(err)
	}
	state := server.Snapshot()
	if state.Power["1"] != "On" || !state.Media["CD1"].Inserted {
		t.Fatalf("unexpected state: %+v", state)
	}
	server.SetFault(http.MethodGet, "/redfish/v1/", redfishtest.Fault{Status: http.StatusServiceUnavailable})
	if err := run(t.Context(), server.URL); err == nil {
		t.Fatal("expected connection failure")
	}
}

func TestDemo(t *testing.T) {
	t.Parallel()
	if err := demo(); err != nil {
		t.Fatal(err)
	}
}

func TestRunFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"systems", http.MethodGet, "/redfish/v1/Systems", http.StatusServiceUnavailable},
		{"reset", http.MethodPost, "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", http.StatusServiceUnavailable},
		{"managers", http.MethodGet, "/redfish/v1/Managers", http.StatusServiceUnavailable},
		{"empty managers", http.MethodGet, "/redfish/v1/Managers", http.StatusOK},
		{"media", http.MethodGet, "/redfish/v1/Managers/1/VirtualMedia", http.StatusServiceUnavailable},
		{"empty media", http.MethodGet, "/redfish/v1/Managers/1/VirtualMedia", http.StatusOK},
		{"insert", http.MethodPost, "/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
			http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := redfishtest.New(t, redfishtest.Config{})
			server.SetFault(tc.method, tc.path, redfishtest.Fault{Status: tc.status})
			if err := run(t.Context(), server.URL); err == nil {
				t.Fatal("expected workflow failure")
			}
		})
	}
	empty := redfishtest.New(t, redfishtest.Config{SystemIDs: []string{}})
	if err := run(t.Context(), empty.URL); err == nil {
		t.Fatal("empty systems accepted")
	}
}
