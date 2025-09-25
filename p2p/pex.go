package p2p

import (
	"fmt"
	"net"
	"strings"
)

type seedEndpoint struct {
	NodeID  string
	Address string
}

func parseSeedList(values []string) []seedEndpoint {
	seeds := make([]seedEndpoint, 0, len(values))
	seen := make(map[string]struct{})
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		nodePart, addrPart, found := strings.Cut(trimmed, "@")
		if !found {
			fmt.Printf("Ignoring seed %q: missing node ID\n", trimmed)
			continue
		}
		node := normalizeHex(nodePart)
		if node == "" {
			fmt.Printf("Ignoring seed %q: empty node ID\n", trimmed)
			continue
		}
		if _, _, err := net.SplitHostPort(strings.TrimSpace(addrPart)); err != nil {
			fmt.Printf("Ignoring seed %q: invalid address: %v\n", trimmed, err)
			continue
		}
		key := node + "@" + strings.TrimSpace(addrPart)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		seeds = append(seeds, seedEndpoint{NodeID: node, Address: strings.TrimSpace(addrPart)})
	}
	return seeds
}
