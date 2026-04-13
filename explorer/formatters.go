package explorer

import "strings"

// TransferLabel returns the explorer label for an outgoing transfer.
func TransferLabel(asset string) string {
	normalized := strings.ToUpper(strings.TrimSpace(asset))
	if normalized == "" {
		normalized = "NHB"
	}
	return "Sent " + normalized
}
