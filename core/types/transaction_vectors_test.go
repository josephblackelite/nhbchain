package types

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// Cross-language test vectors for the transaction signing digest (PL-DC-11).
//
// The node hashes every Type > 0 transaction with the binary NHB_TX_V3_MAINNET
// layout in Transaction.Hash. The TypeScript SDK has to produce the same digest and,
// because ECDSA signing with RFC 6979 is deterministic, the same signature. This
// test generates the vectors from Hash and Sign, checks that the node recovers the
// signer from each of them, and compares them with the checked-in file the
// TypeScript tests read (sdk/ts/test/vectors/tx-hash-vectors.json). If Hash ever
// changes, this test fails until the file is regenerated:
//
//	NHB_UPDATE_TX_VECTORS=1 go test ./core/types -run TestTransactionHashVectors
//
// and the TypeScript SDK is updated to match. The private key below is a throw-away
// test key; it controls nothing.

const txVectorsFile = "../../sdk/ts/test/vectors/tx-hash-vectors.json"

// 32 bytes of 0x11: a test key only.
const txVectorsTestKey = "1111111111111111111111111111111111111111111111111111111111111111"

type txVectorSpec struct {
	name            string
	chainID         string
	txType          TxType
	nonce           uint64
	maxBlockHeight  uint64
	intentExpiry    uint64
	intentRef       []byte
	to              []byte
	value           string
	data            []byte
	gasLimit        uint64
	gasPrice        string
	paymaster       []byte
	merchantAddress string
	deviceID        string
	refundOf        string
}

func txVectorSpecs() []txVectorSpec {
	addr := func(b byte) []byte { return bytes.Repeat([]byte{b}, 20) }
	return []txVectorSpec{
		{name: "nhb transfer, live-size chain id", chainID: "18346390202490284624", txType: TxTypeTransfer, nonce: 3, to: addr(0xaa), value: "1000000000000000000", gasLimit: 25000, gasPrice: "1"},
		{name: "znhb transfer", chainID: "18346390202490284624", txType: TxTypeTransferZNHB, nonce: 0, to: addr(0xbb), value: "12345", gasLimit: 25000, gasPrice: "1"},
		{name: "zero value and no data", chainID: "5130306", txType: TxTypeTransfer, nonce: 7, to: addr(0x01), value: "0", gasLimit: 21000, gasPrice: "1000"},
		{name: "value above 64 bits", chainID: "5130306", txType: TxTypeTransfer, nonce: 1, to: addr(0x02), value: "340282366920938463463374607431768211456", gasLimit: 21000, gasPrice: "7"},
		{name: "short recipient is left-padded", chainID: "5130306", txType: TxTypeTransfer, nonce: 2, to: []byte{0x01, 0x02, 0x03, 0x04, 0x05}, value: "9", gasLimit: 21000, gasPrice: "1"},
		{name: "data payload", chainID: "5130306", txType: TxTypeTransfer, nonce: 4, to: addr(0x03), value: "5", data: bytes.Repeat([]byte{0x5a}, 300), gasLimit: 90000, gasPrice: "2"},
		{name: "paymaster, intent and block bound", chainID: "18346390202490284624", txType: TxTypeTransfer, nonce: 11, maxBlockHeight: 2400000, intentExpiry: 1800000000, intentRef: []byte("order-0001"), to: addr(0x04), value: "250", gasLimit: 30000, gasPrice: "1", paymaster: addr(0xcc)},
		{name: "merchant, device and refund strings (trimmed)", chainID: "18346390202490284624", txType: TxTypeTransfer, nonce: 12, to: addr(0x05), value: "77", gasLimit: 30000, gasPrice: "1", merchantAddress: "  merchant-1 ", deviceID: "device-7", refundOf: " 0xabc "},
		{name: "maximum chain id", chainID: "18446744073709551615", txType: TxTypeTransferZNHB, nonce: 18446744073709551615, to: addr(0xff), value: "1", gasLimit: 18446744073709551615, gasPrice: "18446744073709551616"},
	}
}

func (s txVectorSpec) transaction(t *testing.T) *Transaction {
	t.Helper()
	chain, ok := new(big.Int).SetString(s.chainID, 10)
	if !ok {
		t.Fatalf("%s: chain id %q", s.name, s.chainID)
	}
	value, ok := new(big.Int).SetString(s.value, 10)
	if !ok {
		t.Fatalf("%s: value %q", s.name, s.value)
	}
	gasPrice, ok := new(big.Int).SetString(s.gasPrice, 10)
	if !ok {
		t.Fatalf("%s: gas price %q", s.name, s.gasPrice)
	}
	return &Transaction{
		ChainID: chain, Type: s.txType, Nonce: s.nonce, MaxBlockHeight: s.maxBlockHeight,
		IntentExpiry: s.intentExpiry, IntentRef: s.intentRef, To: s.to, Value: value, Data: s.data,
		GasLimit: s.gasLimit, GasPrice: gasPrice, Paymaster: s.paymaster,
		MerchantAddress: s.merchantAddress, DeviceID: s.deviceID, RefundOf: s.refundOf,
	}
}

type txVectorJSON struct {
	Name            string `json:"name"`
	ChainID         string `json:"chainId"`
	Type            int    `json:"type"`
	Nonce           string `json:"nonce"`
	MaxBlockHeight  string `json:"maxBlockHeight"`
	IntentExpiry    string `json:"intentExpiry"`
	IntentRef       string `json:"intentRef"`
	To              string `json:"to"`
	Value           string `json:"value"`
	Data            string `json:"data"`
	GasLimit        string `json:"gasLimit"`
	GasPrice        string `json:"gasPrice"`
	Paymaster       string `json:"paymaster"`
	MerchantAddress string `json:"merchantAddress"`
	DeviceID        string `json:"deviceId"`
	RefundOf        string `json:"refundOf"`
	Hash            string `json:"hash"`
	R               string `json:"r"`
	S               string `json:"s"`
	V               string `json:"v"`
}

type txVectorFile struct {
	Description string         `json:"description"`
	PrivateKey  string         `json:"privateKey"`
	Address     string         `json:"address"`
	Vectors     []txVectorJSON `json:"vectors"`
}

func u64(v uint64) string { return new(big.Int).SetUint64(v).String() }

func buildTxVectorFile(t *testing.T) txVectorFile {
	t.Helper()
	key, err := crypto.HexToECDSA(txVectorsTestKey)
	if err != nil {
		t.Fatalf("test key: %v", err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Bytes()
	out := txVectorFile{
		Description: "Generated by core/types/transaction_vectors_test.go from Transaction.Hash and Transaction.Sign. Do not edit by hand: regenerate with NHB_UPDATE_TX_VECTORS=1 go test ./core/types -run TestTransactionHashVectors. The key is a throw-away test key.",
		PrivateKey:  txVectorsTestKey,
		Address:     hex.EncodeToString(address),
	}
	for _, spec := range txVectorSpecs() {
		tx := spec.transaction(t)
		hash, err := tx.Hash()
		if err != nil {
			t.Fatalf("%s: hash: %v", spec.name, err)
		}
		if err := tx.Sign(key); err != nil {
			t.Fatalf("%s: sign: %v", spec.name, err)
		}
		// The node must recover the signer from the signature it produced: this is
		// the direction the SDK relies on (a signature over this digest is attributed
		// to the sender's address).
		from, err := tx.From()
		if err != nil {
			t.Fatalf("%s: recover signer: %v", spec.name, err)
		}
		if !bytes.Equal(from, address) {
			t.Fatalf("%s: recovered %x, want %x", spec.name, from, address)
		}
		out.Vectors = append(out.Vectors, txVectorJSON{
			Name: spec.name, ChainID: spec.chainID, Type: int(spec.txType),
			Nonce: u64(spec.nonce), MaxBlockHeight: u64(spec.maxBlockHeight), IntentExpiry: u64(spec.intentExpiry),
			IntentRef: hex.EncodeToString(spec.intentRef), To: hex.EncodeToString(spec.to),
			Value: spec.value, Data: hex.EncodeToString(spec.data),
			GasLimit: u64(spec.gasLimit), GasPrice: spec.gasPrice,
			Paymaster: hex.EncodeToString(spec.paymaster), MerchantAddress: spec.merchantAddress,
			DeviceID: spec.deviceID, RefundOf: spec.refundOf,
			Hash: hex.EncodeToString(hash), R: tx.R.String(), S: tx.S.String(), V: tx.V.String(),
		})
	}
	return out
}

func TestTransactionHashVectors(t *testing.T) {
	want, err := json.MarshalIndent(buildTxVectorFile(t), "", "  ")
	if err != nil {
		t.Fatalf("marshal vectors: %v", err)
	}
	want = append(want, '\n')
	path := filepath.FromSlash(txVectorsFile)
	if os.Getenv("NHB_UPDATE_TX_VECTORS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create vectors directory: %v", err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("write vectors: %v", err)
		}
		t.Logf("wrote %s", path)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with NHB_UPDATE_TX_VECTORS=1)", path, err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatalf("%s is out of date with Transaction.Hash/Sign: regenerate it with NHB_UPDATE_TX_VECTORS=1 go test ./core/types -run TestTransactionHashVectors, then update sdk/ts to match", txVectorsFile)
	}
}

// The vectors must exercise every field the digest covers: changing any one of them
// changes the hash. (If a field were left out of Hash, the TypeScript port could
// ignore it and still match.)
func TestTransactionHashCoversEveryVectorField(t *testing.T) {
	specs := txVectorSpecs()
	base := specs[6] // the one with paymaster, intent and block bound
	baseHash, err := base.transaction(t).Hash()
	if err != nil {
		t.Fatal(err)
	}
	mutate := map[string]func(s *txVectorSpec){
		"chain id":      func(s *txVectorSpec) { s.chainID = "18346390202490284625" },
		"type":          func(s *txVectorSpec) { s.txType = TxTypeTransferZNHB },
		"nonce":         func(s *txVectorSpec) { s.nonce++ },
		"max block":     func(s *txVectorSpec) { s.maxBlockHeight++ },
		"intent expiry": func(s *txVectorSpec) { s.intentExpiry++ },
		"intent ref":    func(s *txVectorSpec) { s.intentRef = []byte("order-0002") },
		"to":            func(s *txVectorSpec) { s.to = bytes.Repeat([]byte{0x09}, 20) },
		"value":         func(s *txVectorSpec) { s.value = "251" },
		"data":          func(s *txVectorSpec) { s.data = []byte{1} },
		"gas limit":     func(s *txVectorSpec) { s.gasLimit++ },
		"gas price":     func(s *txVectorSpec) { s.gasPrice = "2" },
		"paymaster":     func(s *txVectorSpec) { s.paymaster = bytes.Repeat([]byte{0xcd}, 20) },
		"no paymaster":  func(s *txVectorSpec) { s.paymaster = nil },
		"merchant":      func(s *txVectorSpec) { s.merchantAddress = "m" },
		"device":        func(s *txVectorSpec) { s.deviceID = "d" },
		"refund":        func(s *txVectorSpec) { s.refundOf = "r" },
	}
	for name, change := range mutate {
		s := base
		change(&s)
		h, err := s.transaction(t).Hash()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bytes.Equal(h, baseHash) {
			t.Errorf("changing the %s does not change the digest", name)
		}
	}
}
