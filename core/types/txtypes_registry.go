package types

import "fmt"

// txTypeRegistry enumerates every TxType constant this package defines, in
// ascending byte order. It exists so that code and tests which must cover
// "every transaction type" (the block-production poison matrix in
// core/poison_matrix_test.go is the reason it was added) can iterate the set
// instead of hard-coding a list that silently goes stale when a new type is
// added. TestAllTxTypesRegistryIsComplete parses this package's source and
// fails if a TxType constant is declared without a row here, so forgetting
// to register a new type is a test failure, not a coverage gap.
var txTypeRegistry = []struct {
	Type TxType
	Name string
}{
	{TxTypeTransfer, "Transfer"},
	{TxTypeRegisterIdentity, "RegisterIdentity"},
	{TxTypeCreateEscrow, "CreateEscrow"},
	{TxTypeReleaseEscrow, "ReleaseEscrow"},
	{TxTypeRefundEscrow, "RefundEscrow"},
	{TxTypeStake, "Stake"},
	{TxTypeUnstake, "Unstake"},
	{TxTypeHeartbeat, "Heartbeat"},
	{TxTypeLockEscrow, "LockEscrow"},
	{TxTypeDisputeEscrow, "DisputeEscrow"},
	{TxTypeArbitrateRelease, "ArbitrateRelease"},
	{TxTypeArbitrateRefund, "ArbitrateRefund"},
	{TxTypeStakeClaim, "StakeClaim"},
	{TxTypeMint, "Mint"},
	{TxTypeSwapPayoutReceipt, "SwapPayoutReceipt"},
	{TxTypeTransferZNHB, "TransferZNHB"},
	{TxTypeSwapMint, "SwapMint"},
	{TxTypeSwapBurn, "SwapBurn"},
	{TxTypeLendingSupplyNHB, "LendingSupplyNHB"},
	{TxTypeLendingWithdrawNHB, "LendingWithdrawNHB"},
	{TxTypeLendingDepositZNHB, "LendingDepositZNHB"},
	{TxTypeLendingWithdrawZNHB, "LendingWithdrawZNHB"},
	{TxTypeLendingBorrowNHB, "LendingBorrowNHB"},
	{TxTypeLendingRepayNHB, "LendingRepayNHB"},
	{TxTypeBuyZNHB, "BuyZNHB"},
	{TxTypeSetRewardBeneficiary, "SetRewardBeneficiary"},
	{TxTypeRedeemNHB, "RedeemNHB"},
	{TxTypeAttestRedemption, "AttestRedemption"},
	{TxTypeLendingLiquidate, "LendingLiquidate"},
	{TxTypeSwapVoucherMint, "SwapVoucherMint"},
	{TxTypePOSAuthorize, "POSAuthorize"},
	{TxTypePOSCapture, "POSCapture"},
	{TxTypePOSVoid, "POSVoid"},
	{TxTypePOSRegistry, "POSRegistry"},
	{TxTypeBuybackAsk, "BuybackAsk"},
	{TxTypeBuybackRefPrice, "BuybackRefPrice"},
	{TxTypeLendingRefPrice, "LendingRefPrice"},
	{TxTypeGovPropose, "GovPropose"},
	{TxTypeGovVote, "GovVote"},
	{TxTypeGovFinalize, "GovFinalize"},
	{TxTypeGovQueue, "GovQueue"},
	{TxTypeGovExecute, "GovExecute"},
	{TxTypeLendingCreatePool, "LendingCreatePool"},
	{TxTypePotsoStakeLock, "PotsoStakeLock"},
	{TxTypePotsoStakeUnbond, "PotsoStakeUnbond"},
	{TxTypePotsoStakeWithdraw, "PotsoStakeWithdraw"},
	{TxTypeSubscriptionCreatePlan, "SubscriptionCreatePlan"},
	{TxTypeSubscriptionUpdatePlan, "SubscriptionUpdatePlan"},
	{TxTypeSubscriptionSubscribe, "SubscriptionSubscribe"},
	{TxTypeSubscriptionCancel, "SubscriptionCancel"},
	{TxTypeStakeClaimRewards, "StakeClaimRewards"},
	{TxTypeMarketCreateListing, "MarketCreateListing"},
	{TxTypeMarketFillListing, "MarketFillListing"},
	{TxTypeMarketCancelListing, "MarketCancelListing"},
	{TxTypeLendingBorrowFixedTerm, "LendingBorrowFixedTerm"},
	{TxTypeLendingRepayFixedTerm, "LendingRepayFixedTerm"},
	{TxTypeLendingSupplyFixedTerm, "LendingSupplyFixedTerm"},
	{TxTypeExpireEscrow, "ExpireEscrow"},
	{TxTypeDelegatedReleaseEscrow, "DelegatedReleaseEscrow"},
	{TxTypeDelegatedRefundEscrow, "DelegatedRefundEscrow"},
	{TxTypeDelegatedDisputeEscrow, "DelegatedDisputeEscrow"},
	{TxTypeEscrowCreateRealm, "EscrowCreateRealm"},
	{TxTypeEscrowUpdateRealm, "EscrowUpdateRealm"},
	{TxTypeDelegatedCreateEscrow, "DelegatedCreateEscrow"},
	{TxTypeCreateLoyaltyBusiness, "CreateLoyaltyBusiness"},
	{TxTypeLoyaltySetPaymaster, "LoyaltySetPaymaster"},
	{TxTypeLoyaltyAddMerchant, "LoyaltyAddMerchant"},
	{TxTypeLoyaltyRemoveMerchant, "LoyaltyRemoveMerchant"},
	{TxTypeCreateLoyaltyProgram, "CreateLoyaltyProgram"},
	{TxTypeUpdateLoyaltyProgram, "UpdateLoyaltyProgram"},
	{TxTypePauseLoyaltyProgram, "PauseLoyaltyProgram"},
	{TxTypeResumeLoyaltyProgram, "ResumeLoyaltyProgram"},
	{TxTypeSwapVoucherReverse, "SwapVoucherReverse"},
	{TxTypeSwapMarkReconciled, "SwapMarkReconciled"},
	{TxTypeSubmitEvidence, "SubmitEvidence"},
}

// AllTxTypes returns every defined transaction type in ascending byte order.
// The returned slice is a fresh copy and may be modified by the caller.
func AllTxTypes() []TxType {
	out := make([]TxType, len(txTypeRegistry))
	for i, entry := range txTypeRegistry {
		out[i] = entry.Type
	}
	return out
}

// TxTypeName returns the short, stable name of a defined transaction type
// (for example "CreateEscrow" for TxTypeCreateEscrow), or "Unknown(0xNN)" for
// a byte that is not a defined type. It is intended for logs and metrics
// labels, where a bounded, human-readable label is preferable to a raw byte.
func TxTypeName(t TxType) string {
	for _, entry := range txTypeRegistry {
		if entry.Type == t {
			return entry.Name
		}
	}
	return fmt.Sprintf("Unknown(0x%02X)", byte(t))
}
