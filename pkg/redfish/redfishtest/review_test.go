// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package redfishtest_test

import (
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/telekom/t-caas-go-library/pkg/redfish/redfishtest"
)

func TestTrailingJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPatch, "/redfish/v1/Systems/1", `{"Boot":{"BootSourceOverrideTarget":"Cd"}}`},
		{http.MethodPost, "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`},
		{http.MethodPatch, "/redfish/v1/Managers/1/VirtualMedia/CD1", `{"Inserted":true}`},
		{http.MethodPost, "/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia", `{"Inserted":true}`},
		{http.MethodPatch, "/redfish/v1/SessionService", `{"SessionTimeout":300}`},
	} {
		for _, suffix := range []string{" trailing", " {}"} {
			server := redfishtest.New(t, redfishtest.Config{})
			before := server.Snapshot()
			if got := request(t, server, tc.method, tc.path, tc.body+suffix, nil); got != http.StatusBadRequest {
				t.Fatalf("%s %s accepted trailing content: %d", tc.method, tc.path, got)
			}
			after := server.Snapshot()
			before.Requests, after.Requests = nil, nil
			if !reflect.DeepEqual(before, after) {
				t.Fatal("malformed request mutated resource state")
			}
			if got := request(t, server, tc.method, tc.path, tc.body+" \n\t", nil); got != http.StatusNoContent {
				t.Fatal("valid trailing whitespace rejected")
			}
		}
	}
}

func TestInvalidSystemConfig(t *testing.T) {
	t.Parallel()
	for _, cfg := range []redfishtest.Config{
		{SystemCount: -1},
		{SystemIDs: []string{""}},
		{SystemIDs: []string{"a/b"}},
		{SystemIDs: []string{"a?b"}},
		{SystemIDs: []string{"a#b"}},
		{SystemIDs: []string{"a%b"}},
		{SystemIDs: []string{"a b"}},
		{SystemIDs: []string{"same", "same"}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("invalid system configuration did not fail construction")
				}
			}()
			server := redfishtest.Start(cfg)
			server.Close()
		}()
	}
}

func TestAuthenticationChallenge(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{Username: "test", Password: "synthetic"})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/redfish/v1/Systems", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Basic realm="redfishtest"` {
		t.Fatalf("missing Basic challenge: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestMutationETags(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{RequireETag: true})
	path := "/redfish/v1/Systems/1"
	client := connect(t, server, "", "")
	resp, err := client.Get(path)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	old := resp.Header.Get("ETag")
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPatch, path, map[string]any{"Boot": map[string]string{"BootSourceOverrideTarget": "Cd"}}},
		{http.MethodPost, path + "/Actions/ComputerSystem.Reset", map[string]string{"ResetType": "On"}},
	} {
		fresh, getErr := client.Get(path)
		if getErr != nil {
			t.Fatal(getErr)
		}
		io.Copy(io.Discard, fresh.Body)
		fresh.Body.Close()
		var response *http.Response
		var writeErr error
		headers := map[string]string{"If-Match": fresh.Header.Get("ETag")}
		if tc.method == http.MethodPatch {
			response, writeErr = client.PatchWithHeaders(tc.path, tc.body, headers)
		} else {
			response, writeErr = client.PostWithHeaders(tc.path, tc.body, headers)
		}
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.Header.Get("ETag") != "" {
			t.Fatal("mutation advertises stale ETag")
		}
	}
	resp, err = client.Get(path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.Header.Get("ETag") == old || resp.Header.Get("ETag") == "" {
		t.Fatal("GET ETag did not change after mutations")
	}
}
