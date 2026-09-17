package codec

import (
	"testing"

	"google.golang.org/protobuf/types/known/anypb"

	consensusv1 "nhbchain/proto/consensus/v1"
	swapv1 "nhbchain/proto/swap/v1"
)

func TestModuleSwapPayoutReceiptTxSetsAuthorityMetadata(t *testing.T) {
	msg := &swapv1.MsgPayoutReceipt{Authority: " Treasury "}
	packed, err := anypb.New(msg)
	if err != nil {
		t.Fatalf("pack message: %v", err)
	}
	body := &consensusv1.TxEnvelope{ChainId: "1", Nonce: 7}
	tx, err := moduleSwapPayoutReceiptTx(body, packed, msg)
	if err != nil {
		t.Fatalf("build transaction: %v", err)
	}
	if tx.MerchantAddress != "treasury" {
		t.Fatalf("unexpected authority metadata: %q", tx.MerchantAddress)
	}
	hydrateIntentMetadata(tx, body)
	if tx.MerchantAddress != "treasury" {
		t.Fatalf("authority metadata lost after hydration: %q", tx.MerchantAddress)
	}
}
