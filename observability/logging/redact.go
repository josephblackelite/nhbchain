package logging

import (
	"log/slog"
	"sort"
	"strings"
)

// RedactedValue is the canonical placeholder used for sensitive fields in logs.
const RedactedValue = "[REDACTED]"

var redactionAllowlist = map[string]struct{}{
	"service":   {},
	"env":       {},
	"message":   {},
	"severity":  {},
	"timestamp": {},
	"error":     {},
	"reason":    {},
	"component": {},
}

// IsAllowlisted reports whether the provided key is exempt from automatic redaction.
func IsAllowlisted(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	_, ok := redactionAllowlist[normalized]
	return ok
}

// RedactionAllowlist returns a sorted copy of the log keys that are allowed to be emitted
// without redaction. Tests use this to ensure sensitive keys remain masked.
func RedactionAllowlist() []string {
	keys := make([]string, 0, len(redactionAllowlist))
	for key := range redactionAllowlist {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// MaskValue returns the canonical redacted placeholder for non-empty values. Empty values
// are returned unchanged to avoid introducing noise in logs.
func MaskValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	return RedactedValue
}

// MaskField returns a slog.Attr that redacts the supplied value unless the key is
// explicitly allowlisted. The original key casing is preserved for readability.
func MaskField(key, value string) slog.Attr {
	if strings.TrimSpace(value) == "" || IsAllowlisted(key) {
		return slog.String(key, value)
	}
	return slog.String(key, RedactedValue)
}
