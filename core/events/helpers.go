package events

import "strings"

func normalizeAsset(asset string) string {
	trimmed := strings.TrimSpace(asset)
	if trimmed == "" {
		return ""
	}
	return strings.ToUpper(trimmed)
}
