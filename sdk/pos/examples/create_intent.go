package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	posv1 "nhbchain/proto/pos"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// canonicalString builds the NHB Pay canonical string used for signing POS intents.
func canonicalString(intentRef []byte, amount, currency string, expiry uint64, merchant, device, paymaster string) string {
	base := fmt.Sprintf("nhbpay://intent/%s?amount=%s&currency=%s&expiry=%d&merchant=%s", hex.EncodeToString(intentRef), amount, currency, expiry, merchant)
	if device != "" {
		base += "&device=" + device
	}
	if paymaster != "" {
		base += "&paymaster=" + paymaster
	}
	return base
}

// encodeURI assembles the QR/deep-link URI including the signature parameter.
func encodeURI(intentRef []byte, amount, currency string, expiry uint64, merchant, device, paymaster string, signature []byte) string {
	parts := []string{
		"amount=" + urlEscape(amount),
		"currency=" + urlEscape(currency),
		fmt.Sprintf("expiry=%d", expiry),
		"merchant=" + urlEscape(merchant),
	}
	if paymaster != "" {
		parts = append(parts, "paymaster="+urlEscape(paymaster))
	}
	if device != "" {
		parts = append(parts, "device="+urlEscape(device))
	}
	if len(signature) > 0 {
		parts = append(parts, "sig="+hex.EncodeToString(signature))
	}
	return fmt.Sprintf("nhbpay://intent/%s?%s", hex.EncodeToString(intentRef), strings.Join(parts, "&"))
}

// urlEscape performs minimal URL escaping for query parameter values.
func urlEscape(s string) string {
	replacer := strings.NewReplacer(
		" ", "%20",
		"\"", "%22",
		"#", "%23",
		"%", "%25",
		"&", "%26",
		"+", "%2B",
		"/", "%2F",
		"=", "%3D",
		"?", "%3F",
	)
	return replacer.Replace(s)
}

// NHB-AUDIT-S3: this example computes a real ed25519 signature over the
// canonical NHB Pay URI string below (for the QR-code/deep-link flow) but
// never attaches it to the AuthorizePayment call that follows -- the
// MsgAuthorizePayment proto has no signature field to carry it, and the
// gRPC Tx service this calls has no way to verify a caller-supplied
// signature for a real, authorized payment (see rpc/pos_grpc.go's
// submitPayload doc comment for why). This call will fail against a real
// node for exactly that reason -- it is not something this example can
// "fix" locally, since it needs a real, working signed-submission path.
// The actual working pattern -- used by nhbportal's own POS feature today
// -- signs a TxTypePOSAuthorize transaction client-side with the payer's
// own wallet key and submits it via the standard nhb_sendTransaction
// JSON-RPC method, exactly like any other wallet transaction (see
// nhbportal's src/lib/services/walletManager.ts, submitPosAuthorize and
// its signAndAssembleL1Tx call). This example is kept as a reference for
// the canonical-string/QR-code signing shape only, not as a working
// end-to-end authorization flow.
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, "localhost:9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial POS gateway: %v", err)
	}
	defer conn.Close()

	client := posv1.NewTxClient(conn)

	intentRef := make([]byte, 32)
	if _, err := rand.Read(intentRef); err != nil {
		log.Fatalf("generate intent ref: %v", err)
	}

	merchant := "nhb1m0ckmerchantaddre55"
	amount := "15.25"
	currency := "USD"
	expiry := uint64(time.Now().Add(15 * time.Minute).Unix())
	device := "kiosk-7"
	paymaster := "nhb1sponsorship"

	canonical := canonicalString(intentRef, amount, currency, expiry, merchant, device, paymaster)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatalf("generate ed25519 key: %v", err)
	}

	signature := ed25519.Sign(priv, []byte(canonical))
	uri := encodeURI(intentRef, amount, currency, expiry, merchant, device, paymaster, signature)
	// The URI carries merchant and paymaster routing data; avoid logging the literal value.
	_ = uri
	fmt.Println("Generated NHB Pay URI (value omitted; handle securely)")

	resp, err := client.AuthorizePayment(ctx, &posv1.MsgAuthorizePayment{
		Payer:     "nhb1samplepayer",
		Merchant:  merchant,
		Amount:    amount,
		Expiry:    expiry,
		IntentRef: intentRef,
	})
	if err != nil {
		log.Fatalf("authorize payment: %v", err)
	}

	// Authorization identifiers can be replayed; surface them only to trusted storage.
	_ = resp.GetAuthorizationId()
	fmt.Println("Authorization reference retrieved (value omitted; persist securely)")
}
