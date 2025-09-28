package quotas

import (
	"fmt"
	"strings"
)

const (
	quotasPrefix      = "quotas"
	quotasIndexSuffix = "index"
)

func normaliseModule(module string) string {
	return strings.ToLower(strings.TrimSpace(module))
}

func counterKey(module string, epoch uint64, addr []byte) []byte {
	normalised := normaliseModule(module)
	return []byte(fmt.Sprintf("%s/%s/%d/%x", quotasPrefix, normalised, epoch, addr))
}

func epochIndexKey(module string, epoch uint64) []byte {
	normalised := normaliseModule(module)
	return []byte(fmt.Sprintf("%s/%s/%d/%s", quotasPrefix, normalised, epoch, quotasIndexSuffix))
}
