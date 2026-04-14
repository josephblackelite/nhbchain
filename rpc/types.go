package rpc

import (
	"fmt"
	"math/big"
	"strings"

	"nhbchain/core/types"
)

// TransactionResult summarises an executed transaction for RPC consumers.
type TransactionResult struct {
	Hash        string `json:"hash"`
	Type        string `json:"type"`
	Asset       string `json:"asset,omitempty"`
	BlockHash   string `json:"blockHash,omitempty"`
	BlockNumber string `json:"blockNumber,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	Value       string `json:"value,omitempty"`
	Nonce       string `json:"nonce,omitempty"`
	GasLimit    string `json:"gasLimit,omitempty"`
	GasPrice    string `json:"gasPrice,omitempty"`
	Input       string `json:"input,omitempty"`
}

// ReceiptResult reflects the final state of a confirmed transaction.
type ReceiptResult struct {
	TransactionHash string       `json:"transactionHash"`
	BlockHash       string       `json:"blockHash,omitempty"`
	BlockNumber     string       `json:"blockNumber,omitempty"`
	Status          string       `json:"status"`
	GasUsed         string       `json:"gasUsed"`
	Logs            []ReceiptLog `json:"logs"`
}

// ReceiptLog captures a structured event emitted during transaction execution.
type ReceiptLog map[string]string

// ExplorerBlockResult summarises a block for explorer and wallet consumers.
type ExplorerBlockResult struct {
	Height             uint64 `json:"height"`
	Hash               string `json:"hash"`
	Timestamp          int64  `json:"timestamp"`
	Validator          string `json:"validator,omitempty"`
	TxCount            int    `json:"txCount"`
	PrevHash           string `json:"prevHash,omitempty"`
	StateRoot          string `json:"stateRoot,omitempty"`
	TxRoot             string `json:"txRoot,omitempty"`
	ExecutionGraphRoot string `json:"executionGraphRoot,omitempty"`
}

// ExplorerTransactionResult provides an explorer-friendly, chain-authentic
// transaction record with decimal amounts and block timing metadata.
type ExplorerTransactionResult struct {
	ID           string `json:"id"`
	Hash         string `json:"hash"`
	Type         string `json:"type"`
	Asset        string `json:"asset,omitempty"`
	Amount       string `json:"amount,omitempty"`
	DisplayAmount string `json:"displayAmount,omitempty"`
	Decimals     int    `json:"decimals,omitempty"`
	BlockHash    string `json:"blockHash,omitempty"`
	BlockNumber  uint64 `json:"blockNumber,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	Nonce        uint64 `json:"nonce,omitempty"`
	GasLimit     uint64 `json:"gasLimit,omitempty"`
	GasPrice     string `json:"gasPrice,omitempty"`
	Status       string `json:"status,omitempty"`
}

// ExplorerAddressBalances exposes the current chain-derived balances for an
// address in decimal string form.
type ExplorerAddressBalances struct {
	NHB                string `json:"nhb"`
	ZNHB               string `json:"znhb"`
	Stake              string `json:"stake"`
	LockedZNHB         string `json:"lockedZNHB"`
	PendingRewardsZNHB string `json:"pendingRewardsZNHB"`
}

// ExplorerAddressResult describes an address together with current balances and
// historical transactions resolved from chain data.
type ExplorerAddressResult struct {
	Address      string                    `json:"address"`
	Username     string                    `json:"username,omitempty"`
	Label        string                    `json:"label,omitempty"`
	Segment      string                    `json:"segment,omitempty"`
	TxCount      uint64                    `json:"txCount"`
	FirstSeen    int64                     `json:"firstSeen,omitempty"`
	LastSeen     int64                     `json:"lastSeen,omitempty"`
	Balances     ExplorerAddressBalances   `json:"balances"`
	Transactions []ExplorerTransactionResult `json:"transactions"`
}

// ExplorerSearchResult returns one canonical explorer result for a query.
type ExplorerSearchResult struct {
	Query       string                   `json:"query"`
	Kind        string                   `json:"kind"`
	Block       *ExplorerBlockResult     `json:"block,omitempty"`
	Transaction *ExplorerTransactionResult `json:"transaction,omitempty"`
	Address     *ExplorerAddressResult   `json:"address,omitempty"`
}

// ExplorerSeriesPoint is used for explorer charts and time-series summaries.
type ExplorerSeriesPoint struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"value,omitempty"`
	Payments  int     `json:"payments,omitempty"`
	Rewards   float64 `json:"rewards,omitempty"`
}

// ExplorerMerchantResult captures recipient-heavy payment endpoints derived
// from on-chain transfers.
type ExplorerMerchantResult struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Sector      string `json:"sector"`
	Address     string `json:"address,omitempty"`
	Payments24h int    `json:"payments24h"`
	VolumeNHB   string `json:"volumeNHB"`
	Href        string `json:"href,omitempty"`
}

// ExplorerActiveAddressResult captures the most active addresses over the
// recent explorer window together with their current balances.
type ExplorerActiveAddressResult struct {
	Address          string `json:"address"`
	Label            string `json:"label,omitempty"`
	Segment          string `json:"segment"`
	BalanceNHB       string `json:"balanceNHB"`
	BalanceZNHB      string `json:"balanceZNHB,omitempty"`
	RewardsZNHB24h   string `json:"rewardsZNHB24h"`
	TxCount24h       int    `json:"txCount24h"`
}

// ExplorerSnapshotResult aggregates the explorer overview, recent feeds, and
// search-friendly address lists into a single chain-authentic payload.
type ExplorerSnapshotResult struct {
	UpdatedAt          string                      `json:"updatedAt"`
	LatestHeight       uint64                      `json:"latestHeight"`
	ActiveValidators   int                         `json:"activeValidators"`
	CurrentEpoch       uint64                      `json:"currentEpoch"`
	CurrentTime        int64                       `json:"currentTime"`
	MempoolSize        int                         `json:"mempoolSize"`
	CurrentTps         float64                     `json:"currentTps"`
	AverageTps24h      float64                     `json:"averageTps24h"`
	Payments24h        int                         `json:"payments24h"`
	TotalRewards24h    float64                     `json:"totalRewards24h"`
	ZNHBCirculatingSupply string                   `json:"znhbCirculatingSupply"`
	ThroughputHistory  []ExplorerSeriesPoint       `json:"throughputHistory"`
	PaymentsHistory    []ExplorerSeriesPoint       `json:"paymentsHistory"`
	RewardsHistory     []ExplorerSeriesPoint       `json:"rewardsHistory"`
	TopMerchants       []ExplorerMerchantResult    `json:"topMerchants"`
	ActiveAddresses    []ExplorerActiveAddressResult `json:"activeAddresses"`
	LatestBlocks       []ExplorerBlockResult       `json:"latestBlocks"`
	LatestTransactions []ExplorerTransactionResult `json:"latestTransactions"`
}

// hexString formats a uint64 as a 0x-prefixed hexadecimal string.
func hexString(v uint64) string {
	return fmt.Sprintf("0x%x", v)
}

// hexBig formats a big integer as a 0x-prefixed hexadecimal string.
func hexBig(v *big.Int) string {
	if v == nil {
		return "0x0"
	}
	if v.Sign() == 0 {
		return "0x0"
	}
	return fmt.Sprintf("0x%x", v)
}

// formatTxType converts a TxType into a human readable label.
func formatTxType(t types.TxType) string {
	switch t {
	case types.TxTypeTransfer:
		return "Transfer"
	case types.TxTypeTransferZNHB:
		return "TransferZNHB"
	case types.TxTypeRegisterIdentity:
		return "RegisterIdentity"
	case types.TxTypeCreateEscrow:
		return "CreateEscrow"
	case types.TxTypeReleaseEscrow:
		return "ReleaseEscrow"
	case types.TxTypeRefundEscrow:
		return "RefundEscrow"
	case types.TxTypeStake:
		return "Stake"
	case types.TxTypeUnstake:
		return "Unstake"
	case types.TxTypeHeartbeat:
		return "Heartbeat"
	case types.TxTypeLockEscrow:
		return "LockEscrow"
	case types.TxTypeDisputeEscrow:
		return "DisputeEscrow"
	case types.TxTypeArbitrateRelease:
		return "ArbitrateRelease"
	case types.TxTypeArbitrateRefund:
		return "ArbitrateRefund"
	case types.TxTypeStakeClaim:
		return "StakeClaim"
	case types.TxTypeMint:
		return "Mint"
	case types.TxTypeSwapPayoutReceipt:
		return "SwapPayoutReceipt"
	default:
		return fmt.Sprintf("0x%02x", byte(t))
	}
}

// assetLabel returns the canonical asset for transfer-style transactions.
func assetLabel(t types.TxType) string {
	switch t {
	case types.TxTypeTransfer:
		return "NHB"
	case types.TxTypeTransferZNHB:
		return "ZNHB"
	default:
		return ""
	}
}

// ensureHexPrefix normalises hash-like values to use a 0x prefix.
func ensureHexPrefix(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		return trimmed
	}
	return "0x" + trimmed
}
