package wallet

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type fakeRPCClient struct {
	chainID           *big.Int
	nativeBalance     *big.Int
	erc20Balance      *big.Int
	nonce             uint64
	tipCap            *big.Int
	gasPrice          *big.Int
	header            *gethtypes.Header
	estimatedGas      uint64
	receipt           *gethtypes.Receipt
	receiptErr        error
	sentTx            *gethtypes.Transaction
	lastEstimateCall  ethereum.CallMsg
	lastReceiptHash   common.Hash
	lastNonceAddress  common.Address
	lastHeaderRequest *big.Int
}

func (f *fakeRPCClient) ChainID(context.Context) (*big.Int, error) {
	return new(big.Int).Set(f.chainID), nil
}

func (f *fakeRPCClient) BalanceAt(context.Context, common.Address, *big.Int) (*big.Int, error) {
	if f.nativeBalance == nil {
		return big.NewInt(0), nil
	}
	return new(big.Int).Set(f.nativeBalance), nil
}

func (f *fakeRPCClient) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	if f.erc20Balance == nil {
		f.erc20Balance = big.NewInt(0)
	}
	return erc20ABI.Methods["balanceOf"].Outputs.Pack(f.erc20Balance)
}

func (f *fakeRPCClient) PendingNonceAt(_ context.Context, account common.Address) (uint64, error) {
	f.lastNonceAddress = account
	return f.nonce, nil
}

func (f *fakeRPCClient) EstimateGas(_ context.Context, msg ethereum.CallMsg) (uint64, error) {
	f.lastEstimateCall = msg
	return f.estimatedGas, nil
}

func (f *fakeRPCClient) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return new(big.Int).Set(f.tipCap), nil
}

func (f *fakeRPCClient) SuggestGasPrice(context.Context) (*big.Int, error) {
	return new(big.Int).Set(f.gasPrice), nil
}

func (f *fakeRPCClient) HeaderByNumber(_ context.Context, number *big.Int) (*gethtypes.Header, error) {
	if number != nil {
		f.lastHeaderRequest = new(big.Int).Set(number)
	}
	header := *f.header
	return &header, nil
}

func (f *fakeRPCClient) SendTransaction(_ context.Context, tx *gethtypes.Transaction) error {
	f.sentTx = tx
	return nil
}

func (f *fakeRPCClient) TransactionReceipt(_ context.Context, txHash common.Hash) (*gethtypes.Receipt, error) {
	f.lastReceiptHash = txHash
	return f.receipt, f.receiptErr
}

func TestNewEVMHotWalletRejectsMismatchedFromAddress(t *testing.T) {
	client := &fakeRPCClient{chainID: big.NewInt(11155111)}
	_, err := NewEVMHotWallet(client, Config{
		RPCURL:      "https://rpc.example",
		ChainID:     "11155111",
		SignerKey:   testPrivateKeyHex,
		FromAddress: "0x0000000000000000000000000000000000000001",
		Assets: []AssetConfig{{
			Symbol:       "USDC",
			TokenAddress: "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		}},
	})
	if err == nil {
		t.Fatalf("expected from_address mismatch")
	}
}

func TestEVMHotWalletTransferBuildsERC20Transaction(t *testing.T) {
	header := &gethtypes.Header{Number: big.NewInt(100), BaseFee: big.NewInt(1_000_000_000)}
	client := &fakeRPCClient{
		chainID:      big.NewInt(11155111),
		nonce:        7,
		tipCap:       big.NewInt(2_000_000_000),
		gasPrice:     big.NewInt(5_000_000_000),
		header:       header,
		estimatedGas: 65_000,
	}
	hotWallet, err := NewEVMHotWallet(client, Config{
		RPCURL:      "https://rpc.example",
		ChainID:     "11155111",
		SignerKey:   testPrivateKeyHex,
		FromAddress: testFromAddressHex,
		Assets: []AssetConfig{{
			Symbol:       "USDC",
			TokenAddress: "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		}},
	})
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}

	txHash, err := hotWallet.Transfer(context.Background(), "usdc", "0x00000000000000000000000000000000000000aa", big.NewInt(12345))
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if txHash == "" {
		t.Fatalf("expected tx hash")
	}
	if client.sentTx == nil {
		t.Fatalf("expected transaction to be sent")
	}
	if to := client.sentTx.To(); to == nil || !strings.EqualFold(to.Hex(), "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48") {
		t.Fatalf("unexpected token contract destination: %v", to)
	}
	if client.sentTx.Value().Sign() != 0 {
		t.Fatalf("expected erc20 tx value to be zero")
	}
	if client.sentTx.Gas() != 65_000 {
		t.Fatalf("unexpected gas limit %d", client.sentTx.Gas())
	}
	if len(client.sentTx.Data()) == 0 {
		t.Fatalf("expected erc20 calldata")
	}
	status := hotWallet.Status()
	if status.FromAddress != testFromAddressHex {
		t.Fatalf("unexpected from address %q", status.FromAddress)
	}
	if _, ok := status.Assets["USDC"]; !ok {
		t.Fatalf("expected USDC status entry")
	}
}

func TestEVMHotWalletWaitForConfirmations(t *testing.T) {
	client := &fakeRPCClient{
		chainID: big.NewInt(11155111),
		header:  &gethtypes.Header{Number: big.NewInt(12), BaseFee: big.NewInt(1)},
		receipt: &gethtypes.Receipt{
			Status:      gethtypes.ReceiptStatusSuccessful,
			BlockNumber: big.NewInt(10),
		},
	}
	hotWallet, err := NewEVMHotWallet(client, Config{
		RPCURL:      "https://rpc.example",
		ChainID:     "11155111",
		SignerKey:   testPrivateKeyHex,
		FromAddress: testFromAddressHex,
		Assets: []AssetConfig{{
			Symbol:       "USDC",
			TokenAddress: "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		}},
	})
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	txHash := common.HexToHash("0x1234")
	if err := hotWallet.WaitForConfirmations(context.Background(), txHash.Hex(), 3, time.Millisecond); err != nil {
		t.Fatalf("wait for confirmations: %v", err)
	}
	if client.lastReceiptHash != txHash {
		t.Fatalf("unexpected receipt lookup hash %s", client.lastReceiptHash.Hex())
	}
}

func TestEVMHotWalletBalanceReadsERC20Balance(t *testing.T) {
	client := &fakeRPCClient{
		chainID:      big.NewInt(11155111),
		header:       &gethtypes.Header{Number: big.NewInt(100), BaseFee: big.NewInt(1)},
		erc20Balance: big.NewInt(54321),
	}
	hotWallet, err := NewEVMHotWallet(client, Config{
		RPCURL:      "https://rpc.example",
		ChainID:     "11155111",
		SignerKey:   testPrivateKeyHex,
		FromAddress: testFromAddressHex,
		Assets: []AssetConfig{{
			Symbol:       "USDC",
			TokenAddress: "0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		}},
	})
	if err != nil {
		t.Fatalf("new wallet: %v", err)
	}
	balance, err := hotWallet.Balance(context.Background(), "USDC")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.Cmp(big.NewInt(54321)) != 0 {
		t.Fatalf("unexpected erc20 balance %s", balance.String())
	}
}

const testPrivateKeyHex = "4c0883a69102937d6231471b5dbb6204fe512961708279516a7f6d7f7b5bca9f"

var testFromAddressHex = mustAddressHex(testPrivateKeyHex)

func mustAddressHex(privateKeyHex string) string {
	key, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		panic(err)
	}
	return crypto.PubkeyToAddress(key.PublicKey).Hex()
}
