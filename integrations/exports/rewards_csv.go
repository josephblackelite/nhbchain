package exports

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"time"

	"nhbchain/consensus/potso/rewards"
)

// RewardsCSV builds a CSV export for the supplied reward entries and returns the
// serialised data alongside a SHA-256 checksum of the payload.
func RewardsCSV(entries []*rewards.RewardEntry) ([]byte, string, error) {
	buffer := &bytes.Buffer{}
	writer := csv.NewWriter(buffer)
	header := []string{"epoch", "address", "amount", "currency", "status", "generated_at", "checksum"}
	if err := writer.Write(header); err != nil {
		return nil, "", err
	}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		generated := entry.GeneratedAt
		if generated.IsZero() {
			generated = entry.UpdatedAt
		}
		if generated.IsZero() {
			generated = time.Now().UTC()
		}
		record := []string{
			fmt.Sprintf("%d", entry.Epoch),
			"0x" + hex.EncodeToString(entry.Address[:]),
			func() string {
				if entry.Amount == nil {
					return "0"
				}
				return entry.Amount.String()
			}(),
			entry.Currency,
			string(entry.Status),
			generated.UTC().Format(time.RFC3339Nano),
			entry.Checksum,
		}
		if err := writer.Write(record); err != nil {
			return nil, "", err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, "", err
	}
	data := buffer.Bytes()
	checksum := sha256.Sum256(data)
	return data, hex.EncodeToString(checksum[:]), nil
}
