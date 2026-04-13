//go:build swapdstoragetest

package storage

func init() {
	fallbackMemoryDSN = "file:swapd?mode=memory&cache=shared"
}
