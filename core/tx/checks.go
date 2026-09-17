package tx

import (
	"fmt"

	nhbstate "nhbchain/core/state"
)

// CheckPOSRegistry evaluates whether the merchant/device pair is eligible for
// sponsorship based on the POS registry controls. An empty reason string
// indicates the transaction may proceed with sponsorship.
func CheckPOSRegistry(manager *nhbstate.Manager, merchant, device string) (string, error) {
	if manager == nil {
		return "", fmt.Errorf("pos: state manager required")
	}
	normalizedMerchant := nhbstate.NormalizePaymasterMerchant(merchant)
	if normalizedMerchant != "" {
		record, ok, err := manager.POSGetMerchant(normalizedMerchant)
		if err != nil {
			return "", err
		}
		if ok && record != nil && record.Paused {
			return "merchant sponsorship paused", nil
		}
	}

	normalizedDevice := nhbstate.NormalizePaymasterDevice(device)
	if normalizedDevice != "" {
		record, ok, err := manager.POSGetDevice(normalizedDevice)
		if err != nil {
			return "", err
		}
		if ok && record != nil {
			if record.Revoked {
				return "device sponsorship revoked", nil
			}
			if normalizedMerchant != "" {
				bound := nhbstate.NormalizePaymasterMerchant(record.Merchant)
				if bound != "" && bound != normalizedMerchant {
					return fmt.Sprintf("device registered to merchant %s", record.Merchant), nil
				}
			}
		}
	}
	return "", nil
}
