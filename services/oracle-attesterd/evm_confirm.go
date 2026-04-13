package oracleattesterd

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

var transferEventSignature = gethcrypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// EVMClient defines the subset of the Ethereum RPC used by the verifier.
type EVMClient interface {
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*gethtypes.Receipt, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*gethtypes.Header, error)
}

// DialEVMClient initialises an EVM RPC client for the provided endpoint.
func DialEVMClient(endpoint string) (*ethclient.Client, error) {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return nil, fmt.Errorf("evm endpoint required")
	}
	return ethclient.Dial(trimmed)
}

// SettlementVerifier validates that an ERC-20 transfer settled on-chain.
type SettlementVerifier interface {
	Confirm(ctx context.Context, txHash common.Hash, asset Asset, collector common.Address, amount *big.Int, confirmations uint64) error
}

// EVMVerifier implements SettlementVerifier against an Ethereum node.
type EVMVerifier struct {
	client EVMClient
}

// NewEVMVerifier constructs a verifier from an Ethereum client.
func NewEVMVerifier(client EVMClient) *EVMVerifier {
	return &EVMVerifier{client: client}
}

// Confirm checks that the provided transfer has succeeded with the expected semantics.
func (v *EVMVerifier) Confirm(ctx context.Context, txHash common.Hash, asset Asset, collector common.Address, amount *big.Int, confirmations uint64) error {
	if v == nil || v.client == nil {
		return fmt.Errorf("evm verifier not initialised")
	}
	if (txHash == common.Hash{}) {
		return fmt.Errorf("tx hash required")
	}
	if (collector == common.Address{}) {
		return fmt.Errorf("collector address required")
	}
	if amount == nil || amount.Sign() <= 0 {
		return fmt.Errorf("amount must be positive")
	}
	receipt, err := v.client.TransactionReceipt(ctx, txHash)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return fmt.Errorf("transaction %s not found", txHash.Hex())
		}
		return fmt.Errorf("fetch receipt: %w", err)
	}
	if receipt == nil {
		return fmt.Errorf("transaction receipt missing")
	}
	if receipt.Status != gethtypes.ReceiptStatusSuccessful {
		return fmt.Errorf("transaction %s failed", txHash.Hex())
	}
	if confirmations > 0 {
		header, err := v.client.HeaderByNumber(ctx, nil)
		if err != nil {
			return fmt.Errorf("fetch head: %w", err)
		}
		if header == nil || header.Number == nil || receipt.BlockNumber == nil {
			return fmt.Errorf("block metadata unavailable")
		}
		if header.Number.Cmp(receipt.BlockNumber) < 0 {
			return fmt.Errorf("transaction block ahead of head")
		}
		confirmed := new(big.Int).Sub(header.Number, receipt.BlockNumber)
		confirmed.Add(confirmed, big.NewInt(1))
		if confirmed.Cmp(new(big.Int).SetUint64(confirmations)) < 0 {
			return fmt.Errorf("insufficient confirmations: have %s want %d", confirmed.String(), confirmations)
		}
	}
	for _, log := range receipt.Logs {
		if log == nil {
			continue
		}
		if log.Address != asset.Address {
			continue
		}
		if len(log.Topics) < 3 {
			continue
		}
		if log.Topics[0] != transferEventSignature {
			continue
		}
		to := common.BytesToAddress(log.Topics[2].Bytes())
		if to != collector {
			continue
		}
		value := new(big.Int).SetBytes(log.Data)
		if value.Cmp(amount) == 0 {
			return nil
		}
	}
	return fmt.Errorf("no matching transfer for %s", txHash.Hex())
}
