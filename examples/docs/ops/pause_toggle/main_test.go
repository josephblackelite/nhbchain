package main

import (
	"strings"
	"testing"

	"nhbchain/config"
)

// govv1.Pauses carries six modules. The sample used to accept transfer_nhb,
// transfer_znhb and staking, set them locally and then drop them from the message,
// so the operator was told a pause happened that was never sent (PL-DC-41).
func TestSetModulePauseRefusesModulesTheMessageCannotCarry(t *testing.T) {
	for _, module := range []string{"transfer_nhb", "transfer_znhb", "staking", "TRANSFER_NHB"} {
		var p config.Pauses
		if err := setModulePause(&p, module, true); err == nil {
			t.Fatalf("module %q must be refused: the SetPauses message does not carry it", module)
		}
		if p.TransferNHB || p.TransferZNHB || p.Staking {
			t.Fatalf("module %q was set locally even though it was refused", module)
		}
	}
}

func TestSetModulePauseAcceptsTheSixCarriedModules(t *testing.T) {
	for _, module := range []string{"lending", "swap", "escrow", "trade", "loyalty", "potso"} {
		var p config.Pauses
		if err := setModulePause(&p, module, true); err != nil {
			t.Fatalf("module %q: %v", module, err)
		}
	}
}

// Sending the six-module message replaces the stored configuration, so a stored
// transfer or staking pause would be cleared by it. The sample must refuse that.
func TestBuildPausesPayloadRefusesToClearAStoredPauseItCannotCarry(t *testing.T) {
	for name, p := range map[string]config.Pauses{
		"transfer_nhb":  {TransferNHB: true},
		"transfer_znhb": {TransferZNHB: true},
		"staking":       {Staking: true},
	} {
		if _, err := buildPausesPayload(p); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("a stored %s pause must stop the sample, got %v", name, err)
		}
	}
	payload, err := buildPausesPayload(config.Pauses{Lending: true, POTSO: true})
	if err != nil {
		t.Fatalf("six-module state: %v", err)
	}
	if !payload.Lending || !payload.Potso || payload.Swap {
		t.Fatalf("payload does not mirror the state: %+v", payload)
	}
}
