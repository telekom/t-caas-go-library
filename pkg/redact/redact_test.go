// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package redact_test

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/telekom/t-caas-go-library/pkg/redact"
)

func TestURL(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://alice:secret@example.com/image?token=hidden#private", "https://example.com/image"},
		{"https://example.com/path?", "https://example.com/path"},
		{"https://example.com/%E2%82%AC#frag", "https://example.com/%E2%82%AC"},
		{"/relative?x=y", "/relative"}, {"", ""},
		{"https://host/%zz?secret=x", "[redacted invalid URL]"},
		{"https://user:pass@[::1", "[redacted invalid URL]"},
		{"scheme:secret", "[redacted invalid URL]"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := redact.URL(tc.input); got != tc.want {
				t.Fatalf("URL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	raw := "https://alice:secret@host/path?token=hidden#private"
	sentinel := errors.New("root failure")
	for _, message := range []string{
		raw, strings.TrimSuffix(raw, "#private"),
		"https://alice:xxxxx@host/path?token=hidden",
		"https://alice:***@host/path?token=hidden",
		"https://alice:%2A%2A%2A@host/path?token=hidden",
		"alice secret token=hidden private",
	} {
		original := &url.Error{Op: "Get", URL: message, Err: sentinel}
		got := redact.Wrap(original, raw)
		for _, secret := range []string{"alice", "secret", "hidden", "private"} {
			if strings.Contains(got.Error(), secret) {
				t.Errorf("leaked %q in %q", secret, got.Error())
			}
		}
		var urlErr *url.Error
		var sanitized *redact.Error
		if !errors.Is(got, sentinel) || !errors.As(got, &urlErr) || !errors.As(got, &sanitized) {
			t.Fatal("error chain was lost")
		}
	}
	if redact.Wrap(nil, raw) != nil {
		t.Fatal("nil became nonnil")
	}
	err := errors.New("plain")
	if redact.Wrap(err, "").Error() != "plain" {
		t.Fatal("empty URL modified diagnostic")
	}
	malformed := "https://user:password@[::1?query=hidden#fragment"
	got := redact.Wrap(errors.New(malformed), malformed)
	if strings.Contains(got.Error(), "password") {
		t.Fatal("malformed URL leaked")
	}
	for _, raw := range []string{"https://user:password@host/\n", "scheme:password"} {
		_, parseErr := url.Parse(raw)
		if parseErr == nil {
			parseErr = errors.New(raw)
		}
		got := redact.Wrap(parseErr, raw)
		if strings.Contains(got.Error(), "password") || !errors.Is(got, parseErr) {
			t.Fatal("escaped malformed URL leaked or lost error", got)
		}
	}
}

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		input string
		limit int
		want  string
	}{
		{"abc", 10, "abc"}, {"abc", 0, ""}, {"abc", -1, ""},
		{"abcdef", 1, "."}, {"abcdef", 3, "..."},
		{"éééé", 6, "é..."}, {"éééé", 4, "..."},
		{"bad\xff", 20, "bad\uFFFD"},
	} {
		got := redact.Truncate(tc.input, tc.limit)
		if got != tc.want || !utf8.ValidString(got) || len(got) > max(tc.limit, 0) {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.input, tc.limit, got, tc.want)
		}
	}
}

func TestWrapQueryParameters(t *testing.T) {
	raw := "https://host/path?first=public&token=hidden%20value&token=other+secret#fragment"
	for _, message := range []string{
		"token=hidden%20value", "token=hidden value",
		"hidden%20value", "hidden value",
		"token=other+secret", "token=other secret", "other secret",
	} {
		original := errors.New(message)
		got := redact.Wrap(original, raw)
		if got.Error() != "[redacted]" || !errors.Is(got, original) {
			t.Errorf("Wrap(%q) = %q or lost cause", message, got.Error())
		}
	}
	original := errors.New("token=%zz flag")
	got := redact.Wrap(original, "https://host/path?token=%zz&flag&empty=")
	if got.Error() != "[redacted] [redacted]" || !errors.Is(got, original) {
		t.Errorf("malformed query component = %q or lost cause", got.Error())
	}
}

func TestWrapDecodedFragment(t *testing.T) {
	original := errors.New("access token")
	got := redact.Wrap(original, "https://host/path#access%20token")
	if got.Error() != "[redacted]" || !errors.Is(got, original) {
		t.Errorf("decoded fragment = %q or lost cause", got.Error())
	}
}

func TestWrapEncodedCredentials(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		message string
	}{
		{"https://a%6Cice:s%65cret@host/path", "a%6Cice"},
		{"https://a%6Cice:s%65cret@host/path", "s%65cret"},
		{"HTTPS://a%6Cice:s%65cret@host/path", "s%65cret"},
		{"https://a%6Cice:s%65cret@host/path", "alice secret"},
		{"//a%6Cice:s%65cret@host/path?email=other@example.com", "s%65cret"},
		{"https://a%40b:p%3Ass@host/path", "a%40b"},
		{"https://a%40b:p%3Ass@host/path", "p%3Ass"},
		{"https://a%6Cice@host/path", "a%6Cice"},
	} {
		original := errors.New(tc.message)
		got := redact.Wrap(original, tc.raw)
		if strings.Contains(got.Error(), tc.message) || !errors.Is(got, original) {
			t.Errorf("Wrap(%q, %q) = %q or lost cause", tc.message, tc.raw, got.Error())
		}
	}
}

type diagnosticError string

func (e diagnosticError) Error() string { return string(e) }

func TestErrorFormatting(t *testing.T) {
	raw := "https://alice:secret@host/path?token=hidden"
	for _, original := range []error{
		&url.Error{Op: "Get", URL: raw, Err: errors.New("failed")},
		diagnosticError(raw),
	} {
		wrapped := redact.Wrap(original, raw)
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			got := fmt.Sprintf(format, wrapped)
			for _, secret := range []string{"alice", "secret", "hidden"} {
				if strings.Contains(got, secret) {
					t.Errorf("%s leaked %q in %q", format, secret, got)
				}
			}
		}
	}
}

func TestDiagnostic(t *testing.T) {
	got := redact.Diagnostic("failed:\n https://user:pass@host/file?secret=yes#private). \tother", 100)
	if got != "failed: https://host/file). other" {
		t.Fatalf("unexpected diagnostic: %q", got)
	}
}

func TestDiagnosticNetworkPath(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"failed: //alice:secret@example.com/file?token=hidden#private).", "failed: //example.com/file)."},
		{"failed: //alice:secret@[::1", "failed: [redacted invalid URL]"},
		{"failed: https://alice:secret@[::1]", "failed: https://[::1]"},
		{"failed: (https://alice:secret@[::1]).", "failed: (https://[::1])."},
		{"failed: [//alice:secret@[::1]].", "failed: [//[::1]]."},
		{"plain relative/file text", "plain relative/file text"},
	} {
		if got := redact.Diagnostic(tc.input, 100); got != tc.want {
			t.Errorf("Diagnostic(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func ExampleURL() {
	fmt.Println(redact.URL("https://user:password@example.com/image?token=secret#private"))
	// Output: https://example.com/image
}
