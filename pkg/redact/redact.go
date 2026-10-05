// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package redact provides best-effort diagnostic redaction. It is not a
// comprehensive secret scrubber: paths, unknown credentials and arbitrary text
// may still contain secrets. Unwrap exposes the original error to errors.Is/As;
// callers must not log that original error.
package redact

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// URL strips user information, query parameters and fragments. Invalid URLs
// are replaced entirely. Relative paths are supported; opaque URLs are hidden
// because their opaque portion may contain credentials.
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return "[redacted invalid URL]"
	}
	u.User = nil
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	u.ForceQuery = false
	return u.String()
}

// Error is a sanitized error retaining its original cause.
type Error struct {
	message string
	cause   error
}

// Error returns only the sanitized diagnostic.
func (e *Error) Error() string { return e.message }

// GoString returns only the sanitized diagnostic for Go-syntax formatting.
func (e *Error) GoString() string { return e.message }

// Unwrap returns the original cause for errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.cause }

// Wrap sanitizes known URLs (including common password-masked variants) and
// their credential, query and fragment components, including individual query
// parameters and values in encoded and decoded forms. A nil error remains nil.
// Malformed or opaque URLs hide the entire diagnostic to avoid parse-error leaks.
// Image references are not URLs; keep their validation and diagnostics at the caller.
func Wrap(err error, rawURLs ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, raw := range rawURLs {
		if raw == "" {
			continue
		}
		candidates := []string{raw}
		u, parseErr := url.Parse(raw)
		if parseErr != nil || u.Opaque != "" {
			message = "[redacted invalid URL error]"
			continue
		}
		candidates = append(candidates, u.String(), u.Redacted(), u.Fragment)
		u.Fragment, u.RawFragment = "", ""
		candidates = append(candidates, u.String(), u.Redacted())
		if u.User != nil {
			username := u.User.Username()
			password, _ := u.User.Password()
			candidates = append(candidates, u.User.String(), username, password)
			authority := raw
			if u.Scheme != "" {
				_, authority, _ = strings.Cut(raw, ":")
			}
			authority = strings.TrimPrefix(authority, "//")
			if end := strings.IndexAny(authority, "/?#"); end >= 0 {
				authority = authority[:end]
			}
			rawUserInfo := authority[:strings.LastIndex(authority, "@")]
			for _, userInfo := range []string{rawUserInfo, u.User.String()} {
				encodedUsername, encodedPassword, _ := strings.Cut(userInfo, ":")
				candidates = append(candidates, userInfo, encodedUsername, encodedPassword)
			}
			for _, mask := range []string{"***", "xxxxx"} {
				u.User = url.UserPassword(username, mask)
				candidates = append(candidates, u.String())
			}
		}
		_, fragment, _ := strings.Cut(raw, "#")
		beforeFragment, _, _ := strings.Cut(raw, "#")
		_, query, _ := strings.Cut(beforeFragment, "?")
		candidates = append(candidates, beforeFragment, query, fragment)
		for parameter := range strings.SplitSeq(query, "&") {
			_, value, _ := strings.Cut(parameter, "=")
			for _, component := range []string{parameter, value} {
				candidates = append(candidates, component)
				if decoded, decodeErr := url.QueryUnescape(component); decodeErr == nil {
					candidates = append(candidates, decoded)
				}
			}
		}
		sort.SliceStable(candidates, func(i, j int) bool { return len(candidates[i]) > len(candidates[j]) })
		// One replacement pass prevents a credential substring from corrupting
		// an already-sanitized replacement.
		pairs := make([]string, 0, 2*len(candidates))
		for _, candidate := range candidates {
			if candidate != "" {
				pairs = append(pairs, candidate, "[redacted]")
			}
		}
		message = strings.NewReplacer(pairs...).Replace(message)
	}
	return &Error{message: message, cause: err}
}

// Truncate returns valid UTF-8 bounded by maxBytes, including a "..." suffix
// when shortened. Nonpositive limits return an empty string.
func Truncate(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maxBytes {
		return value
	}
	if maxBytes <= 3 {
		return strings.Repeat(".", maxBytes)
	}
	cut := maxBytes - 3
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "..."
}

var embeddedURL = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*:)?//[^\s"'<>]+`)

// Diagnostic strips credentials from embedded URLs (including scheme-relative
// network paths), collapses whitespace and bounds the result. Non-URL secrets
// must be removed by the caller.
func Diagnostic(value string, maxBytes int) string {
	value = embeddedURL.ReplaceAllStringFunc(value, func(raw string) string {
		trimmed := strings.TrimRight(raw, ".,;:)}")
		unmatched := strings.Count(trimmed, "]") - strings.Count(trimmed, "[")
		for unmatched > 0 && strings.HasSuffix(trimmed, "]") {
			trimmed = strings.TrimRight(strings.TrimSuffix(trimmed, "]"), ".,;:)}")
			unmatched--
		}
		return URL(trimmed) + strings.TrimPrefix(raw, trimmed)
	})
	return Truncate(strings.Join(strings.Fields(value), " "), maxBytes)
}
