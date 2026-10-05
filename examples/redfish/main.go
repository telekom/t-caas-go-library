// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command redfish powers on a system and inserts a virtual CD using a synthetic
// server; no physical hardware is needed.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"

	"github.com/telekom/t-caas-go-library/pkg/redfish/redfishtest"
)

func run(ctx context.Context, endpoint string) error {
	httpClient := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	defer httpClient.CloseIdleConnections()
	client, err := gofish.ConnectContext(ctx, gofish.ClientConfig{
		Endpoint: endpoint, BasicAuth: true, HTTPClient: httpClient, NoModifyTransport: true,
	})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer client.Logout()
	systems, err := client.GetService().Systems()
	if err != nil {
		return fmt.Errorf("list systems: %w", err)
	}
	if len(systems) != 1 {
		return fmt.Errorf("expected one synthetic system, got %d", len(systems))
	}
	if _, err := systems[0].Reset(schemas.OnResetType); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	managers, err := client.GetService().Managers()
	if err != nil {
		return fmt.Errorf("list managers: %w", err)
	}
	if len(managers) != 1 {
		return fmt.Errorf("expected one synthetic manager, got %d", len(managers))
	}
	media, err := managers[0].VirtualMedia()
	if err != nil {
		return fmt.Errorf("list virtual media: %w", err)
	}
	if len(media) == 0 {
		return fmt.Errorf("expected synthetic virtual media")
	}
	inserted, protected := true, true
	if _, err := media[0].InsertMedia(&schemas.VirtualMediaInsertMediaParameters{
		Image: "https://192.0.2.1/example.iso", Inserted: &inserted, WriteProtected: &protected,
	}); err != nil {
		return fmt.Errorf("insert media: %w", err)
	}
	return nil
}

func demo() error {
	server := redfishtest.Start(redfishtest.Config{})
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return run(ctx, server.URL)
}

func main() {
	if err := demo(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("system On; virtual CD inserted")
}
