package mint

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func newStateProcessor(t *testing.T) *core.StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := core.NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	return sp
}

func mintTransaction(t *testing.T, voucher core.MintVoucher, signer *crypto.PrivateKey) *types.Transaction {
	t.Helper()
	canonical, err := voucher.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical voucher: %v", err)
	}
	digest := ethcrypto.Keccak256(canonical)
	signature, err := ethcrypto.Sign(digest, signer.PrivateKey)
	if err != nil {
		t.Fatalf("sign voucher: %v", err)
	}
	payload := struct {
		Voucher   core.MintVoucher `json:"voucher"`
		Signature string           `json:"signature"`
	}{Voucher: voucher, Signature: "0x" + hex.EncodeToString(signature)}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMint,
		Data:     data,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
}

func TestMintEmissionCapEnforced(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		token    string
		paramKey string
	}{
		{name: "NHB", token: "NHB", paramKey: governance.ParamKeyMintNHBMaxEmissionPerYearWei},
		{name: "ZNHB", token: "ZNHB", paramKey: governance.ParamKeyMintZNHBMaxEmissionPerYearWei},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sp := newStateProcessor(t)
			manager := nhbstate.NewManager(sp.Trie)

			signer, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate signer: %v", err)
			}
			if err := manager.SetRole("MINTER_"+tc.token, signer.PubKey().Address().Bytes()); err != nil {
				t.Fatalf("assign minter role: %v", err)
			}
			if err := manager.ParamStoreSet(tc.paramKey, []byte("1000")); err != nil {
				t.Fatalf("set emission cap: %v", err)
			}

			recipientKey, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate recipient: %v", err)
			}
			rawRecipient := recipientKey.PubKey().Address().Bytes()
			var recipientAddr crypto.Address
			if tc.token == "ZNHB" {
				recipientAddr = crypto.MustNewAddress(crypto.ZNHBPrefix, rawRecipient)
			} else {
				recipientAddr = crypto.MustNewAddress(crypto.NHBPrefix, rawRecipient)
			}

			blockTime := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
			sp.BeginBlock(1, blockTime)
			defer sp.EndBlock()

			buildVoucher := func(invoice string, amount *big.Int) core.MintVoucher {
				return core.MintVoucher{
					InvoiceID: invoice,
					Recipient: recipientAddr.String(),
					Token:     tc.token,
					Amount:    amount.String(),
					ChainID:   core.MintChainID,
					Expiry:    blockTime.Add(2 * time.Hour).Unix(),
				}
			}

			firstAmount := big.NewInt(600)
			firstVoucher := buildVoucher(fmt.Sprintf("invoice-%s-1", tc.token), firstAmount)
			if err := sp.ApplyTransaction(mintTransaction(t, firstVoucher, signer)); err != nil {
				t.Fatalf("first mint failed: %v", err)
			}
			account, err := manager.GetAccount(recipientAddr.Bytes())
			if err != nil {
				t.Fatalf("load account: %v", err)
			}
			switch tc.token {
			case "NHB":
				if account.BalanceNHB.Cmp(firstAmount) != 0 {
					t.Fatalf("unexpected NHB balance: %s", account.BalanceNHB)
				}
			case "ZNHB":
				if account.BalanceZNHB.Cmp(firstAmount) != 0 {
					t.Fatalf("unexpected ZNHB balance: %s", account.BalanceZNHB)
				}
			}
			ytd, err := manager.MintEmissionYTD(tc.token, uint32(blockTime.Year()))
			if err != nil {
				t.Fatalf("load ytd after first mint: %v", err)
			}
			if ytd.Cmp(firstAmount) != 0 {
				t.Fatalf("unexpected ytd after first mint: %s", ytd)
			}

			secondAmount := big.NewInt(400)
			secondVoucher := buildVoucher(fmt.Sprintf("invoice-%s-2", tc.token), secondAmount)
			if err := sp.ApplyTransaction(mintTransaction(t, secondVoucher, signer)); err != nil {
				t.Fatalf("second mint failed: %v", err)
			}
			account, err = manager.GetAccount(recipientAddr.Bytes())
			if err != nil {
				t.Fatalf("reload account: %v", err)
			}
			expectedTotal := new(big.Int).Add(firstAmount, secondAmount)
			switch tc.token {
			case "NHB":
				if account.BalanceNHB.Cmp(expectedTotal) != 0 {
					t.Fatalf("unexpected NHB balance after second mint: %s", account.BalanceNHB)
				}
			case "ZNHB":
				if account.BalanceZNHB.Cmp(expectedTotal) != 0 {
					t.Fatalf("unexpected ZNHB balance after second mint: %s", account.BalanceZNHB)
				}
			}
			ytd, err = manager.MintEmissionYTD(tc.token, uint32(blockTime.Year()))
			if err != nil {
				t.Fatalf("load ytd after second mint: %v", err)
			}
			if ytd.Cmp(expectedTotal) != 0 {
				t.Fatalf("unexpected ytd after second mint: %s", ytd)
			}

			thirdAmount := big.NewInt(1)
			thirdVoucher := buildVoucher(fmt.Sprintf("invoice-%s-3", tc.token), thirdAmount)
			err = sp.ApplyTransaction(mintTransaction(t, thirdVoucher, signer))
			if err == nil || err != core.ErrMintEmissionCapExceeded {
				t.Fatalf("expected emission cap error, got %v", err)
			}
			account, err = manager.GetAccount(recipientAddr.Bytes())
			if err != nil {
				t.Fatalf("reload account after rejection: %v", err)
			}
			switch tc.token {
			case "NHB":
				if account.BalanceNHB.Cmp(expectedTotal) != 0 {
					t.Fatalf("unexpected NHB balance after rejection: %s", account.BalanceNHB)
				}
			case "ZNHB":
				if account.BalanceZNHB.Cmp(expectedTotal) != 0 {
					t.Fatalf("unexpected ZNHB balance after rejection: %s", account.BalanceZNHB)
				}
			}
			ytd, err = manager.MintEmissionYTD(tc.token, uint32(blockTime.Year()))
			if err != nil {
				t.Fatalf("load ytd after rejection: %v", err)
			}
			if ytd.Cmp(expectedTotal) != 0 {
				t.Fatalf("unexpected ytd after rejection: %s", ytd)
			}
		})
	}
}
