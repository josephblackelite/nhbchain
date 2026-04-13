package wallet

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	defaultGasTipCapWei = int64(1_500_000_000)
	defaultNativeGas    = uint64(21_000)
)

var erc20ABI = mustParseERC20ABI()

// AssetConfig binds an asset symbol to its settlement route.
type AssetConfig struct {
	Symbol       string
	TokenAddress string
	Native       bool
}

// Config captures the parameters required to operate an EVM treasury hot wallet.
type Config struct {
	RPCURL      string
	ChainID     string
	SignerKey   string
	FromAddress string
	Assets      []AssetConfig
}

// RPCClient defines the subset of Ethereum JSON-RPC used by payoutd.
type RPCClient interface {
	ChainID(ctx context.Context) (*big.Int, error)
	BalanceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error)
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	PendingNonceAt(ctx context.Context, account common.Address) (uint64, error)
	EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error)
	SuggestGasTipCap(ctx context.Context) (*big.Int, error)
	SuggestGasPrice(ctx context.Context) (*big.Int, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*gethtypes.Header, error)
	SendTransaction(ctx context.Context, tx *gethtypes.Transaction) error
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*gethtypes.Receipt, error)
}

// EVMHotWallet executes native and ERC-20 payouts from a treasury hot wallet.
type EVMHotWallet struct {
	client     RPCClient
	closeFn    func()
	privateKey *ecdsa.PrivateKey
	from       common.Address
	chainID    *big.Int
	rpcURL     string
	assets     map[string]assetRoute
}

type assetRoute struct {
	Native       bool
	TokenAddress common.Address
}

// DialEVMHotWallet connects to the configured RPC endpoint and builds a treasury wallet.
func DialEVMHotWallet(ctx context.Context, cfg Config) (*EVMHotWallet, error) {
	client, err := ethclient.DialContext(ctx, strings.TrimSpace(cfg.RPCURL))
	if err != nil {
		return nil, fmt.Errorf("dial wallet rpc: %w", err)
	}
	wallet, err := NewEVMHotWallet(client, cfg)
	if err != nil {
		client.Close()
		return nil, err
	}
	wallet.closeFn = client.Close
	return wallet, nil
}

// NewEVMHotWallet constructs a wallet around the provided RPC client.
func NewEVMHotWallet(client RPCClient, cfg Config) (*EVMHotWallet, error) {
	if client == nil {
		return nil, fmt.Errorf("wallet rpc client required")
	}
	chainID, err := parseChainID(cfg.ChainID)
	if err != nil {
		return nil, err
	}
	privateKey, err := parsePrivateKey(cfg.SignerKey)
	if err != nil {
		return nil, err
	}
	from := gethcrypto.PubkeyToAddress(privateKey.PublicKey)
	if trimmed := strings.TrimSpace(cfg.FromAddress); trimmed != "" {
		expected := common.HexToAddress(trimmed)
		if expected == (common.Address{}) {
			return nil, fmt.Errorf("wallet from_address invalid")
		}
		if expected != from {
			return nil, fmt.Errorf("wallet from_address does not match signer")
		}
	}
	clientChainID, err := client.ChainID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("fetch wallet rpc chain id: %w", err)
	}
	if clientChainID == nil || clientChainID.Cmp(chainID) != 0 {
		return nil, fmt.Errorf("wallet rpc chain id mismatch: have %v want %s", clientChainID, chainID.String())
	}
	assets := make(map[string]assetRoute, len(cfg.Assets))
	for _, raw := range cfg.Assets {
		symbol := strings.ToUpper(strings.TrimSpace(raw.Symbol))
		if symbol == "" {
			return nil, fmt.Errorf("wallet asset symbol required")
		}
		if _, exists := assets[symbol]; exists {
			return nil, fmt.Errorf("wallet asset %s configured more than once", symbol)
		}
		route := assetRoute{Native: raw.Native}
		if raw.Native {
			if strings.TrimSpace(raw.TokenAddress) != "" {
				return nil, fmt.Errorf("wallet asset %s cannot set token_address when native=true", symbol)
			}
		} else {
			route.TokenAddress = common.HexToAddress(strings.TrimSpace(raw.TokenAddress))
			if route.TokenAddress == (common.Address{}) {
				return nil, fmt.Errorf("wallet asset %s token_address invalid", symbol)
			}
		}
		assets[symbol] = route
	}
	if len(assets) == 0 {
		return nil, fmt.Errorf("wallet assets required")
	}
	return &EVMHotWallet{
		client:     client,
		privateKey: privateKey,
		from:       from,
		chainID:    chainID,
		rpcURL:     strings.TrimSpace(cfg.RPCURL),
		assets:     assets,
	}, nil
}

// Transfer sends the requested payout through the configured treasury route.
func (w *EVMHotWallet) Transfer(ctx context.Context, asset, destination string, amount *big.Int) (string, error) {
	if w == nil || w.client == nil {
		return "", fmt.Errorf("wallet not initialised")
	}
	if amount == nil || amount.Sign() <= 0 {
		return "", fmt.Errorf("transfer amount must be positive")
	}
	symbol := strings.ToUpper(strings.TrimSpace(asset))
	route, ok := w.assets[symbol]
	if !ok {
		return "", fmt.Errorf("asset %s not configured", symbol)
	}
	to := common.HexToAddress(strings.TrimSpace(destination))
	if to == (common.Address{}) {
		return "", fmt.Errorf("destination invalid")
	}
	nonce, err := w.client.PendingNonceAt(ctx, w.from)
	if err != nil {
		return "", fmt.Errorf("fetch pending nonce: %w", err)
	}
	header, err := w.client.HeaderByNumber(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("fetch latest header: %w", err)
	}
	tipCap, err := w.client.SuggestGasTipCap(ctx)
	if err != nil || tipCap == nil || tipCap.Sign() <= 0 {
		tipCap = big.NewInt(defaultGasTipCapWei)
	}

	var (
		tx *gethtypes.Transaction
	)
	if route.Native {
		call := ethereum.CallMsg{From: w.from, To: &to, Value: new(big.Int).Set(amount)}
		tx, err = w.newTx(ctx, nonce, to, amount, nil, call, defaultNativeGas, header, tipCap)
	} else {
		payload, packErr := erc20ABI.Pack("transfer", to, amount)
		if packErr != nil {
			return "", fmt.Errorf("pack erc20 transfer: %w", packErr)
		}
		call := ethereum.CallMsg{From: w.from, To: &route.TokenAddress, Data: payload}
		tx, err = w.newTx(ctx, nonce, route.TokenAddress, big.NewInt(0), payload, call, 0, header, tipCap)
	}
	if err != nil {
		return "", err
	}
	if err := w.client.SendTransaction(ctx, tx); err != nil {
		return "", fmt.Errorf("send transaction: %w", err)
	}
	return tx.Hash().Hex(), nil
}

// Balance returns the current on-chain hot-wallet balance for the configured asset.
func (w *EVMHotWallet) Balance(ctx context.Context, asset string) (*big.Int, error) {
	if w == nil || w.client == nil {
		return nil, fmt.Errorf("wallet not initialised")
	}
	symbol := strings.ToUpper(strings.TrimSpace(asset))
	route, ok := w.assets[symbol]
	if !ok {
		return nil, fmt.Errorf("asset %s not configured", symbol)
	}
	if route.Native {
		balance, err := w.client.BalanceAt(ctx, w.from, nil)
		if err != nil {
			return nil, fmt.Errorf("fetch native balance: %w", err)
		}
		if balance == nil {
			return big.NewInt(0), nil
		}
		return new(big.Int).Set(balance), nil
	}
	payload, err := erc20ABI.Pack("balanceOf", w.from)
	if err != nil {
		return nil, fmt.Errorf("pack erc20 balanceOf: %w", err)
	}
	raw, err := w.client.CallContract(ctx, ethereum.CallMsg{To: &route.TokenAddress, Data: payload}, nil)
	if err != nil {
		return nil, fmt.Errorf("call erc20 balanceOf: %w", err)
	}
	values, err := erc20ABI.Unpack("balanceOf", raw)
	if err != nil {
		return nil, fmt.Errorf("decode erc20 balanceOf: %w", err)
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("decode erc20 balanceOf: unexpected return values")
	}
	value, ok := values[0].(*big.Int)
	if !ok || value == nil {
		return nil, fmt.Errorf("decode erc20 balanceOf: unexpected value type")
	}
	return new(big.Int).Set(value), nil
}

func (w *EVMHotWallet) newTx(ctx context.Context, nonce uint64, to common.Address, value *big.Int, data []byte, call ethereum.CallMsg, fallbackGas uint64, header *gethtypes.Header, tipCap *big.Int) (*gethtypes.Transaction, error) {
	gasLimit := fallbackGas
	if gasLimit == 0 {
		estimated, err := w.client.EstimateGas(ctx, call)
		if err != nil {
			return nil, fmt.Errorf("estimate gas: %w", err)
		}
		gasLimit = estimated
	}
	if gasLimit == 0 {
		return nil, fmt.Errorf("gas limit required")
	}
	if header != nil && header.BaseFee != nil {
		feeCap := new(big.Int).Mul(header.BaseFee, big.NewInt(2))
		feeCap.Add(feeCap, tipCap)
		signer := gethtypes.LatestSignerForChainID(w.chainID)
		tx := gethtypes.MustSignNewTx(w.privateKey, signer, &gethtypes.DynamicFeeTx{
			ChainID:   w.chainID,
			Nonce:     nonce,
			GasTipCap: tipCap,
			GasFeeCap: feeCap,
			Gas:       gasLimit,
			To:        &to,
			Value:     value,
			Data:      data,
		})
		return tx, nil
	}
	gasPrice, err := w.client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("suggest gas price: %w", err)
	}
	signer := gethtypes.LatestSignerForChainID(w.chainID)
	tx := gethtypes.MustSignNewTx(w.privateKey, signer, &gethtypes.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      gasLimit,
		To:       &to,
		Value:    value,
		Data:     data,
	})
	return tx, nil
}

// WaitForConfirmations blocks until the transaction is confirmed or fails.
func (w *EVMHotWallet) WaitForConfirmations(ctx context.Context, txHash string, confirmations int, pollInterval time.Duration) error {
	if w == nil || w.client == nil {
		return fmt.Errorf("wallet not initialised")
	}
	hash := common.HexToHash(strings.TrimSpace(txHash))
	if hash == (common.Hash{}) {
		return fmt.Errorf("transaction hash invalid")
	}
	if confirmations <= 0 {
		confirmations = 1
	}
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		receipt, err := w.client.TransactionReceipt(ctx, hash)
		if err == nil && receipt != nil {
			if receipt.Status != gethtypes.ReceiptStatusSuccessful {
				return fmt.Errorf("transaction %s failed", hash.Hex())
			}
			if confirmations <= 1 {
				return nil
			}
			head, headErr := w.client.HeaderByNumber(ctx, nil)
			if headErr != nil {
				return fmt.Errorf("fetch latest header: %w", headErr)
			}
			if head == nil || head.Number == nil || receipt.BlockNumber == nil {
				return fmt.Errorf("confirmation metadata unavailable")
			}
			if head.Number.Cmp(receipt.BlockNumber) >= 0 {
				confirmed := new(big.Int).Sub(head.Number, receipt.BlockNumber)
				confirmed.Add(confirmed, big.NewInt(1))
				if confirmed.Cmp(big.NewInt(int64(confirmations))) >= 0 {
					return nil
				}
			}
		} else if err != nil && !errors.Is(err, ethereum.NotFound) {
			return fmt.Errorf("fetch receipt: %w", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Status reports the active treasury wallet wiring.
func (w *EVMHotWallet) Status() Status {
	status := Status{
		Mode:        "evm",
		RPCURL:      w.rpcURL,
		FromAddress: w.from.Hex(),
		Assets:      make(map[string]AssetStatus, len(w.assets)),
	}
	if w.chainID != nil {
		status.ChainID = w.chainID.String()
	}
	for symbol, route := range w.assets {
		entry := AssetStatus{Native: route.Native}
		if !route.Native {
			entry.TokenAddress = route.TokenAddress.Hex()
		}
		status.Assets[symbol] = entry
	}
	return status
}

// Close releases the underlying RPC client if the wallet owns it.
func (w *EVMHotWallet) Close() error {
	if w == nil || w.closeFn == nil {
		return nil
	}
	w.closeFn()
	return nil
}

func parseChainID(raw string) (*big.Int, error) {
	chainID, ok := new(big.Int).SetString(strings.TrimSpace(raw), 0)
	if !ok || chainID.Sign() <= 0 {
		return nil, fmt.Errorf("wallet chain_id invalid")
	}
	return chainID, nil
}

func parsePrivateKey(raw string) (*ecdsa.PrivateKey, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "0x")
	if trimmed == "" {
		return nil, fmt.Errorf("wallet signer key required")
	}
	key, err := gethcrypto.HexToECDSA(trimmed)
	if err != nil {
		return nil, fmt.Errorf("wallet signer key invalid: %w", err)
	}
	return key, nil
}

func mustParseERC20ABI() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(`[
{"type":"function","name":"transfer","inputs":[{"name":"to","type":"address"},{"name":"value","type":"uint256"}],"outputs":[{"name":"","type":"bool"}],"stateMutability":"nonpayable"},
{"type":"function","name":"balanceOf","inputs":[{"name":"account","type":"address"}],"outputs":[{"name":"","type":"uint256"}],"stateMutability":"view"}
]`))
	if err != nil {
		panic(err)
	}
	return parsed
}
