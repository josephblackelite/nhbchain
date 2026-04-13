package events

import (
	"math/big"
	"testing"
)

func TestTokenSupplyEvent(t *testing.T) {
	evt := TokenSupply{
		Token:  "znhb",
		Total:  big.NewInt(5000),
		Delta:  big.NewInt(250),
		Reason: SupplyReasonMint,
	}.Event()
	if evt == nil {
		t.Fatalf("expected event")
	}
	if evt.Type != TypeTokenSupply {
		t.Fatalf("unexpected type: %s", evt.Type)
	}
	if evt.Attributes["token"] != "ZNHB" {
		t.Fatalf("unexpected token attr: %s", evt.Attributes["token"])
	}
	if evt.Attributes["total"] != "5000" || evt.Attributes["delta"] != "250" {
		t.Fatalf("unexpected attrs: %+v", evt.Attributes)
	}
	if evt.Attributes["reason"] != SupplyReasonMint {
		t.Fatalf("unexpected reason: %s", evt.Attributes["reason"])
	}
}
