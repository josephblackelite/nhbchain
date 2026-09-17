package exports

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"nhbchain/consensus/potso/rewards"
)

// RewardsJSONL builds a JSON Lines export for the supplied reward entries and
// returns the serialised payload alongside a checksum.
func RewardsJSONL(entries []*rewards.RewardEntry) ([]byte, string, error) {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
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
		payload := map[string]interface{}{
			"epoch":   entry.Epoch,
			"address": "0x" + hex.EncodeToString(entry.Address[:]),
			"amount": func() string {
				if entry.Amount == nil {
					return "0"
				}
				return entry.Amount.String()
			}(),
			"currency":     entry.Currency,
			"status":       string(entry.Status),
			"generated_at": generated.UTC().Format(time.RFC3339Nano),
			"checksum":     entry.Checksum,
		}
		if err := encoder.Encode(payload); err != nil {
			return nil, "", err
		}
	}
	data := buffer.Bytes()
	checksum := sha256.Sum256(data)
	return data, hex.EncodeToString(checksum[:]), nil
}
