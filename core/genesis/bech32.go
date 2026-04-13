package genesis

import (
	"fmt"

	"github.com/btcsuite/btcutil/bech32"
)

func ParseBech32Account(addr string) ([20]byte, error) {
	var out [20]byte
	hrp, data, err := bech32.Decode(addr)
	if err != nil {
		return out, fmt.Errorf("decode bech32 account: %w", err)
	}
	if hrp != "nhb" && hrp != "znhb" {
		return out, fmt.Errorf("decode bech32 account: unsupported hrp %q", hrp)
	}
	decoded, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil {
		return out, fmt.Errorf("decode bech32 account: %w", err)
	}
	if len(decoded) != len(out) {
		return out, fmt.Errorf("decode bech32 account: invalid address length %d", len(decoded))
	}
	copy(out[:], decoded)
	return out, nil
}
