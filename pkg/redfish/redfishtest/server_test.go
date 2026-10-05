// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package redfishtest_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/telekom/t-caas-go-library/pkg/redfish/redfishtest"
)

func request(t *testing.T, server *redfishtest.Server, method, path, body string, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func TestProtocol(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{})
	for _, tc := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodGet, "/redfish/v1/", "", http.StatusOK},
		{http.MethodGet, "/redfish/v1/Systems", "", http.StatusOK},
		{http.MethodGet, "/redfish/v1/Systems/1", "", http.StatusOK},
		{http.MethodGet, "/redfish/v1/Systems/missing", "", http.StatusNotFound},
		{http.MethodDelete, "/redfish/v1/Systems/1", "", http.StatusNotFound},
		{http.MethodPost, "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"Off"}`, http.StatusBadRequest},
		{http.MethodPost, "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", "{", http.StatusBadRequest},
		{http.MethodPatch, "/redfish/v1/Systems/1", "{", http.StatusBadRequest},
		{http.MethodPatch, "/redfish/v1/Systems/1", `{"Boot":{"BootSourceOverrideTarget":"Cd"}}`, http.StatusNoContent},
		{http.MethodPost, "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`, http.StatusNoContent},
		{http.MethodGet, "/redfish/v1/Managers", "", http.StatusOK},
		{http.MethodGet, "/redfish/v1/Managers/1", "", http.StatusOK},
		{http.MethodGet, "/redfish/v1/Managers/1/VirtualMedia", "", http.StatusOK},
		{http.MethodGet, "/redfish/v1/Managers/1/VirtualMedia/CD1", "", http.StatusOK},
		{http.MethodGet, "/redfish/v1/Managers/1/VirtualMedia/missing", "", http.StatusNotFound},
		{http.MethodDelete, "/redfish/v1/Managers/1/VirtualMedia/CD1", "", http.StatusNotFound},
		{http.MethodPatch, "/redfish/v1/Managers/1/VirtualMedia/CD1", "{", http.StatusBadRequest},
		{http.MethodPatch, "/redfish/v1/Managers/1/VirtualMedia/CD1", `{"Inserted":true}`, http.StatusNoContent},
		{http.MethodPost, "/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia", "{", http.StatusBadRequest},
		{http.MethodPost, "/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
			`{"Image":"https://192.0.2.1/test.iso","Inserted":true}`, http.StatusNoContent},
		{http.MethodPost, "/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia", "", http.StatusNoContent},
		{http.MethodGet, "/redfish/v1/SessionService", "", http.StatusOK},
		{http.MethodPatch, "/redfish/v1/SessionService", "{", http.StatusBadRequest},
		{http.MethodPatch, "/redfish/v1/SessionService", `{"SessionTimeout":300}`, http.StatusNoContent},
		{http.MethodDelete, "/redfish/v1/SessionService", "", http.StatusNotFound},
		{http.MethodGet, "/missing", "", http.StatusNotFound},
	} {
		if got := request(t, server, tc.method, tc.path, tc.body, nil); got != tc.status {
			t.Fatalf("%s %s got %d, want %d", tc.method, tc.path, got, tc.status)
		}
	}
	snapshot := server.Snapshot()
	snapshot.Power["1"] = "corrupt"
	snapshot.Media["CD1"] = redfishtest.Media{Image: "corrupt"}
	snapshot.Resets[0].ResetType = "corrupt"
	snapshot.Requests["corrupt"] = 1
	snapshot.Boot["1"] = "corrupt"
	state := server.Snapshot()
	if state.Power["1"] != "On" || state.Boot["1"] != "Cd" ||
		state.Resets[0].ResetType != "On" || state.SessionTimeout != 300 {
		t.Fatal("snapshot aliasing or state machine failure")
	}
}

func TestFaultsAndETags(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{RequireETag: true})
	path := "/redfish/v1/Systems/1"
	for _, headers := range []map[string]string{nil, {"If-Match": `"stale"`}} {
		if status := request(t, server, http.MethodPatch, path, `{}`, headers); status != http.StatusPreconditionFailed {
			t.Fatal("missing/stale ETag accepted")
		}
	}
	server.SetFault(http.MethodGet, path, redfishtest.Fault{Status: http.StatusServiceUnavailable, Count: 2})
	for range 2 {
		if status := request(t, server, http.MethodGet, path, "", nil); status != http.StatusServiceUnavailable {
			t.Fatal("fault not injected")
		}
	}
	if status := request(t, server, http.MethodGet, path, "", nil); status != http.StatusOK {
		t.Fatal("fault did not expire")
	}
	server.SetFault(http.MethodGet, path, redfishtest.Fault{Delay: time.Millisecond})
	if status := request(t, server, http.MethodGet, path, "", nil); status != http.StatusOK {
		t.Fatal("delay fault failed")
	}
	server.SetFault(http.MethodGet, path, redfishtest.Fault{Delay: time.Hour})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("delay ignored cancellation")
	}
}

func TestConcurrentInspection(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{})
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			server.Snapshot()
			server.SetFault(http.MethodGet, "/unused", redfishtest.Fault{})
		})
	}
	wg.Wait()
}

func ExampleStart() {
	server := redfishtest.Start(redfishtest.Config{SystemIDs: []string{"demo"}})
	defer server.Close()
	fmt.Println(server.Snapshot().Power["demo"])
	// Output: Off
}
