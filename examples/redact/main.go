// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Command redact demonstrates safe URL diagnostics.
package main

import (
	"fmt"

	"github.com/telekom/t-caas-go-library/pkg/redact"
)

func main() {
	fmt.Println(redact.URL("https://alice:secret@example.com/file?token=secret#private"))
	fmt.Println(redact.Diagnostic("download failed:\nhttps://alice:secret@example.com/file?token=secret", 80))
}
