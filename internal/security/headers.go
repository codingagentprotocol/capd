package security

import (
	"fmt"
	"net/http"
	"net/textproto"
	"strings"
)

const maxHeaderValueLen = 8 * 1024

var forbiddenIncomingHeaders = map[string]struct{}{
	"Authorization":       {},
	"Cookie":              {},
	"Openai-Api-Key":      {},
	"Proxy-Authorization": {},
	"Set-Cookie":          {},
	"X-Api-Key":           {},
}

var sensitiveHeaders = map[string]struct{}{
	"Authorization":       {},
	"Cookie":              {},
	"Openai-Api-Key":      {},
	"Proxy-Authorization": {},
	"Set-Cookie":          {},
	"X-Api-Key":           {},
}

// CanonicalHeaderName returns the stable form used for policy lookups.
func CanonicalHeaderName(name string) string {
	return textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
}

// ValidateHeaderValue rejects values that could smuggle extra headers or
// explode logs/protocol events. It intentionally accepts ordinary UTF-8 text:
// upstream SDKs and browser clients sometimes include non-ASCII product names.
func ValidateHeaderValue(value string) error {
	if len(value) > maxHeaderValueLen {
		return fmt.Errorf("header value exceeds %d bytes", maxHeaderValueLen)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("header value contains a newline")
	}
	return nil
}

// ValidateHeader validates one header field before capd accepts it from a
// client-controlled surface.
func ValidateHeader(name, value string) error {
	canonical := CanonicalHeaderName(name)
	if canonical == "" {
		return fmt.Errorf("header name is empty")
	}
	if _, denied := forbiddenIncomingHeaders[canonical]; denied {
		return fmt.Errorf("header %q is not accepted from clients", canonical)
	}
	return ValidateHeaderValue(value)
}

// ValidateHeaders validates a complete incoming header map. It is intentionally
// conservative: any forbidden header means the request should not cross a trust
// boundary as user-supplied metadata.
func ValidateHeaders(headers http.Header) error {
	for name, values := range headers {
		canonical := CanonicalHeaderName(name)
		if _, denied := forbiddenIncomingHeaders[canonical]; denied {
			return fmt.Errorf("header %q is not accepted from clients", canonical)
		}
		for _, value := range values {
			if err := ValidateHeaderValue(value); err != nil {
				return fmt.Errorf("header %q: %w", canonical, err)
			}
		}
	}
	return nil
}

// IsSensitiveHeader reports whether a header should be removed or masked in
// logs, protocol events, and diagnostics.
func IsSensitiveHeader(name string) bool {
	_, ok := sensitiveHeaders[CanonicalHeaderName(name)]
	return ok
}

// RedactHeaders returns a copy suitable for logging. Sensitive fields are
// masked and account identifiers are shortened because they are not secrets but
// are still privacy-sensitive.
func RedactHeaders(headers http.Header) map[string][]string {
	out := make(map[string][]string, len(headers))
	for name, values := range headers {
		canonical := CanonicalHeaderName(name)
		redacted := make([]string, 0, len(values))
		for _, value := range values {
			switch {
			case IsSensitiveHeader(canonical):
				redacted = append(redacted, "<redacted>")
			case strings.EqualFold(canonical, "Chatgpt-Account-Id"):
				redacted = append(redacted, RedactMiddle(value))
			default:
				redacted = append(redacted, value)
			}
		}
		out[canonical] = redacted
	}
	return out
}

// RedactMiddle keeps short diagnostic hints without exposing full identifiers.
func RedactMiddle(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "<redacted>"
	}
	return value[:4] + "..." + value[len(value)-4:]
}
