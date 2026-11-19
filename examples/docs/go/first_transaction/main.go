// Command first_transaction builds, signs and submits one envelope through the
// consensus gRPC service.
//
// The envelope carries a point-of-sale merchant registration, one of the few
// module payloads the node's envelope decoder accepts (consensus/codec: the POS
// messages and the swap payout receipt). Other modules, lending included, are
// not carried by this envelope path and are rejected with "unsupported module
// payload type". Whether the node then applies the registration depends on the
// authority and the on-chain rules; this sample shows the envelope path only.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	consensusv1 "nhbchain/proto/consensus/v1"
	posv1 "nhbchain/proto/pos"

	"nhbchain/crypto"
	"nhbchain/rpc/txchainid"
	cons "nhbchain/sdk/consensus"
)

// buildSignedEnvelope wraps a merchant registration for the key's own address in
// an envelope for the given network and signs it. chainID is the decimal id the node
// reports as txChainId in nhb_getNetworkStats (see resolveChainID); it is required
// because the envelope is bound to one network.
func buildSignedEnvelope(key *crypto.PrivateKey, chainID string, expiresAt uint64) (*consensusv1.SignedTxEnvelope, string, error) {
	chainID = strings.TrimSpace(chainID)
	if chainID == "" {
		return nil, "", fmt.Errorf("a chain id is required (the node reports it as txChainId in nhb_getNetworkStats)")
	}
	sender := key.PubKey().Address().String()
	msg := &posv1.MsgRegisterMerchant{
		Authority:    sender,
		MerchantAddr: sender,
		Nonce:        1,
		ExpiresAt:    expiresAt,
		ChainId:      chainID,
	}
	envelope, err := cons.NewTx(msg, 1, chainID, "", "", "", "first transaction demo")
	if err != nil {
		return nil, "", fmt.Errorf("build envelope: %w", err)
	}
	signed, err := cons.Sign(envelope, key)
	if err != nil {
		return nil, "", fmt.Errorf("sign envelope: %w", err)
	}
	return signed, sender, nil
}

// resolveChainID returns the decimal chain id to put in the envelope: the one the node reports as txChainId in the
// public nhb_getNetworkStats call. The id a node accepts changes once, at the chain's activation height, so the
// sample asks instead of carrying a number. NHB_CHAIN_ID, when set, is an explicit choice that skips the question.
// If the node cannot say, nothing is signed.
func resolveChainID(ctx context.Context, explicit, rpcURL string) (string, error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return v, nil
	}
	id, err := txchainid.NewReader(txchainid.HTTPFetcher(nil, rpcURL, ""), 0).ChainID(ctx)
	if err != nil {
		return "", fmt.Errorf("read the chain id from %s: %w", rpcURL, err)
	}
	return id.String(), nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoint := os.Getenv("CONSENSUSD_GRPC_ADDR")
	if endpoint == "" {
		endpoint = "localhost:9090"
	}

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}
	rpcURL := os.Getenv("NHB_RPC_URL")
	if rpcURL == "" {
		rpcURL = "http://localhost:8545"
	}
	chainID, err := resolveChainID(ctx, os.Getenv("NHB_CHAIN_ID"), rpcURL)
	if err != nil {
		log.Fatal(err)
	}
	signed, sender, err := buildSignedEnvelope(key, chainID, uint64(time.Now().Add(time.Hour).Unix()))
	if err != nil {
		log.Fatal(err)
	}

	client, err := cons.Dial(ctx, endpoint, cons.WithInsecure())
	if err != nil {
		log.Fatalf("dial consensus: %v", err)
	}
	defer client.Close()

	if err := client.SubmitEnvelope(ctx, signed); err != nil {
		log.Fatalf("submit envelope: %v", err)
	}
	fmt.Printf("submitted a merchant registration envelope for %s\n", sender)
}
