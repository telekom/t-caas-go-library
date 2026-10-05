// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package redfishtest_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"

	"github.com/telekom/t-caas-go-library/pkg/redfish/redfishtest"
)

func connect(t *testing.T, server *redfishtest.Server, username, password string) *gofish.APIClient {
	t.Helper()
	client, err := gofish.ConnectContext(t.Context(), gofish.ClientConfig{
		Endpoint: server.URL, Username: username, Password: password, BasicAuth: true,
		HTTPClient: server.Client(), NoModifyTransport: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Logout)
	return client
}

func waitPower(t *testing.T, server *redfishtest.Server, state string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for server.Snapshot().Power["1"] != state {
		if time.Now().After(deadline) {
			t.Fatal("reset transition did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGofishPower(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{
		TLS: true, Username: "test", Password: "synthetic", RequireETag: true, ResetDelay: time.Millisecond,
	})
	client := connect(t, server, "test", "synthetic")
	systems, err := client.GetService().Systems()
	if err != nil || len(systems) != 1 {
		t.Fatalf("systems: %v, %v", systems, err)
	}
	system := systems[0]
	if system.Boot.BootSourceOverrideEnabled != schemas.DisabledBootSourceOverrideEnabled {
		t.Fatal("initial override is not disabled")
	}
	if err := system.SetBoot(&schemas.Boot{
		BootSourceOverrideEnabled: schemas.OnceBootSourceOverrideEnabled,
		BootSourceOverrideTarget:  schemas.CdBootSource,
	}); err != nil {
		t.Fatal(err)
	}
	system, err = schemas.GetComputerSystem(client, system.ODataID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.Reset(schemas.OnResetType); err != nil {
		t.Fatal(err)
	}
	waitPower(t, server, "On")
	if server.Snapshot().Boot["1"] != "Cd" || server.Snapshot().BootEnabled["1"] != "Once" {
		t.Fatal("boot update missing")
	}
	system, err = schemas.GetComputerSystem(client, system.ODataID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.Reset(schemas.ForceOffResetType); err != nil {
		t.Fatal(err)
	}
	waitPower(t, server, "Off")
}

func TestGofishMediaAndSession(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{RequireETag: true})
	client := connect(t, server, "", "")
	managers, err := client.GetService().Managers()
	if err != nil || len(managers) != 1 {
		t.Fatalf("managers: %v, %v", managers, err)
	}
	media, err := managers[0].VirtualMedia()
	if err != nil || len(media) != 2 {
		t.Fatalf("media: %v, %v", media, err)
	}
	inserted, protected := true, true
	if _, err := media[0].InsertMedia(&schemas.VirtualMediaInsertMediaParameters{
		Image: "https://192.0.2.1/test.iso", Inserted: &inserted, WriteProtected: &protected,
	}); err != nil {
		t.Fatal(err)
	}
	slot, err := schemas.GetVirtualMedia(client, media[0].ODataID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slot.EjectMedia(); err != nil {
		t.Fatal(err)
	}
	session, err := client.GetService().SessionService()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Patch(session.ODataID, struct{ SessionTimeout int }{300}); err != nil {
		t.Fatal(err)
	}
	if server.Snapshot().SessionTimeout != 300 || server.Snapshot().Media["CD1"].Inserted {
		t.Fatal("state does not reflect gofish mutations")
	}
}

func TestGofishAuthenticationAndFaults(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{Username: "test", Password: "synthetic"})
	client := connect(t, server, "test", "wrong")
	if _, err := client.GetService().Systems(); err == nil {
		t.Fatal("unauthorized discovery succeeded")
	}
	client = connect(t, server, "test", "synthetic")
	path := "/redfish/v1/Systems/1"
	server.SetFault(http.MethodGet, path, redfishtest.Fault{
		Status: http.StatusServiceUnavailable, Count: 1,
		MessageIDs: []string{"Base.1.0.ServiceTemporarilyUnavailable"},
	})
	_, err := schemas.GetComputerSystem(client, path)
	var protocolErr *schemas.Error
	if !errors.As(err, &protocolErr) || len(protocolErr.ExtendedInfos) != 1 {
		t.Fatalf("structured fault lost: %v", err)
	}
	server.SetFault(http.MethodGet, path, redfishtest.Fault{Delay: time.Hour})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = schemas.GetComputerSystem(client.WithContext(ctx), path)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestPartialPatchAndCollections(t *testing.T) {
	t.Parallel()
	server := redfishtest.New(t, redfishtest.Config{DisableMediaActions: true})
	path := "/redfish/v1/Managers/1/VirtualMedia/CD1"
	for _, body := range []string{
		`{"Image":"https://192.0.2.1/test.iso","Inserted":true,"WriteProtected":true}`,
		`{"Inserted":false}`,
	} {
		if got := request(t, server, http.MethodPatch, path, body, nil); got != http.StatusNoContent {
			t.Fatalf("patch: %d", got)
		}
	}
	state := server.Snapshot().Media["CD1"]
	if state.Inserted || !state.WriteProtected || state.Image == "" {
		t.Fatal("partial patch overwrote omitted fields")
	}
	if got := request(t, server, http.MethodPatch, path, `{"Image":123}`, nil); got != http.StatusBadRequest {
		t.Fatal("invalid image accepted")
	}
	if got := request(t, server, http.MethodPatch, path, `{"Image":null}`, nil); got != http.StatusNoContent {
		t.Fatal("null image rejected")
	}
	if server.Snapshot().Media["CD1"].Image != "" {
		t.Fatal("null image did not clear")
	}
	client := connect(t, server, "", "")
	slot, err := schemas.GetVirtualMedia(client, path)
	if err != nil || slot.SupportsMediaInsert || slot.SupportsMediaEject {
		t.Fatalf("disabled actions: %v, %v", slot, err)
	}
	if got := request(t, server, http.MethodPost, path+"/Actions/VirtualMedia.InsertMedia", `{}`, nil); got != http.StatusNotFound {
		t.Fatal("disabled action accepted")
	}
	for _, ids := range [][]string{{}, {"rack-01", "slot_2"}} {
		collection := redfishtest.New(t, redfishtest.Config{SystemIDs: ids})
		systems, listErr := connect(t, collection, "", "").GetService().Systems()
		if listErr != nil || len(systems) != len(ids) {
			t.Fatalf("collection: %v, %v", systems, listErr)
		}
	}
}
