package core

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"nhbchain/core/types"
)

// The poison matrix: block production must survive EVERY transaction type
// carrying EVERY kind of hostile payload. Before the containment layer, feeding
// CreateBlock a single garbage-payload transaction of 66 of the 75 defined
// types returned an error (a whole-block abort), and admission-time simulation
// was the only thing standing in front of it. These tests pin the structural
// guarantee instead: one bad transaction plus one valid transfer still
// produces a block that contains the transfer and validates.
//
// Adding a transaction type without a row in poisonMatrixRows fails
// TestPoisonMatrixHasRowForEveryTxType, so a new type cannot ship without being
// put through this matrix.

// poisonMatrixRow describes one transaction type for the matrix.
type poisonMatrixRow struct {
	// Senderless mirrors types.RequiresSignature == false: the type carries its
	// own attestation and has no account, nonce or per-sender quota, so the
	// mempool's per-sender limits do not apply to it.
	Senderless bool
	// Family names the residual poison-pill family (from the design's list) the
	// type belongs to, or "" when it has none beyond the generic garbage case:
	//   a escrow status races      b wall-clock reads      c governance triggers
	//   d market fill/cancel       e BuyZNHB shared curve  f lending sentinels
	//   g POS authorisation state  h senderless evidence   i balance drift
	//   j price-proof / voucher    (k lifecycle is not per-type)
	Family string
	Note   string
}

var poisonMatrixRows = map[types.TxType]poisonMatrixRow{
	types.TxTypeTransfer:               {Family: "i", Note: "balance drift between admission and build"},
	types.TxTypeRegisterIdentity:       {Note: "username collision"},
	types.TxTypeCreateEscrow:           {Family: "b", Note: "reads the wall clock and stores it (creation time)"},
	types.TxTypeReleaseEscrow:          {Family: "a", Note: "status race with refund/dispute"},
	types.TxTypeRefundEscrow:           {Family: "ab", Note: "status race; deadline compares the wall clock"},
	types.TxTypeStake:                  {Note: "staking pause"},
	types.TxTypeUnstake:                {Note: "staking pause"},
	types.TxTypeHeartbeat:              {Note: "rate limit / replay"},
	types.TxTypeLockEscrow:             {Family: "a", Note: "status race"},
	types.TxTypeDisputeEscrow:          {Family: "a", Note: "status race"},
	types.TxTypeArbitrateRelease:       {Family: "a", Note: "status race"},
	types.TxTypeArbitrateRefund:        {Family: "a", Note: "status race"},
	types.TxTypeStakeClaim:             {Note: "unbonding maturity"},
	types.TxTypeMint:                   {Senderless: true, Family: "j", Note: "voucher expiry / invoice reuse"},
	types.TxTypeSwapPayoutReceipt:      {Note: "payout receipt"},
	types.TxTypeTransferZNHB:           {Family: "i", Note: "balance drift between admission and build"},
	types.TxTypeSwapMint:               {Note: "native swap mint"},
	types.TxTypeSwapBurn:               {Note: "native swap burn"},
	types.TxTypeLendingSupplyNHB:       {Family: "f", Note: "lending caps and oracle staleness"},
	types.TxTypeLendingWithdrawNHB:     {Family: "f", Note: "lending liquidity sentinels"},
	types.TxTypeLendingDepositZNHB:     {Family: "f", Note: "lending caps"},
	types.TxTypeLendingWithdrawZNHB:    {Family: "f", Note: "lending health check"},
	types.TxTypeLendingBorrowNHB:       {Family: "f", Note: "per-block borrow cap, oracle staleness"},
	types.TxTypeLendingRepayNHB:        {Family: "f", Note: "lending debt state"},
	types.TxTypeBuyZNHB:                {Family: "e", Note: "shared curve and sale pool"},
	types.TxTypeSetRewardBeneficiary:   {Note: "validator reward beneficiary"},
	types.TxTypeRedeemNHB:              {Note: "redeem burn"},
	types.TxTypeAttestRedemption:       {Note: "redemption attestation"},
	types.TxTypeLendingLiquidate:       {Family: "f", Note: "health factor race"},
	types.TxTypeSwapVoucherMint:        {Senderless: true, Family: "j", Note: "price proof, signer registry, caps"},
	types.TxTypePOSAuthorize:           {Family: "g", Note: "authorisation state"},
	types.TxTypePOSCapture:             {Family: "g", Note: "capture/void race, expiry"},
	types.TxTypePOSVoid:                {Family: "g", Note: "capture/void race, expiry"},
	types.TxTypePOSRegistry:            {Family: "g", Note: "registry update"},
	types.TxTypeBuybackAsk:             {Note: "buyback ask"},
	types.TxTypeBuybackRefPrice:        {Senderless: true, Note: "stale or duplicate epoch"},
	types.TxTypeLendingRefPrice:        {Senderless: true, Note: "stale timestamp"},
	types.TxTypeGovPropose:             {Family: "c", Note: "governance"},
	types.TxTypeGovVote:                {Family: "c", Note: "vote after close"},
	types.TxTypeGovFinalize:            {Family: "c", Note: "permissionless trigger, double finalize"},
	types.TxTypeGovQueue:               {Family: "c", Note: "permissionless trigger, double queue"},
	types.TxTypeGovExecute:             {Family: "c", Note: "permissionless trigger, double execute"},
	types.TxTypeLendingCreatePool:      {Family: "f", Note: "pool creation"},
	types.TxTypePotsoStakeLock:         {Note: "potso stake"},
	types.TxTypePotsoStakeUnbond:       {Note: "potso stake"},
	types.TxTypePotsoStakeWithdraw:     {Note: "potso stake"},
	types.TxTypeSubscriptionCreatePlan: {Note: "subscriptions"},
	types.TxTypeSubscriptionUpdatePlan: {Note: "subscriptions"},
	types.TxTypeSubscriptionSubscribe:  {Note: "subscriptions"},
	types.TxTypeSubscriptionCancel:     {Note: "subscriptions"},
	types.TxTypeStakeClaimRewards:      {Note: "staking rewards claim"},
	types.TxTypeMarketCreateListing:    {Family: "d", Note: "market"},
	types.TxTypeMarketFillListing:      {Family: "d", Note: "fill/cancel race"},
	types.TxTypeMarketCancelListing:    {Family: "d", Note: "fill/cancel race"},
	types.TxTypeLendingBorrowFixedTerm: {Family: "f", Note: "fixed-term lending"},
	types.TxTypeLendingRepayFixedTerm:  {Family: "f", Note: "fixed-term lending"},
	types.TxTypeLendingSupplyFixedTerm: {Family: "f", Note: "fixed-term lending"},
	types.TxTypeExpireEscrow:           {Family: "a", Note: "status race; uses block time"},
	types.TxTypeDelegatedReleaseEscrow: {Family: "a", Note: "delegated status race"},
	types.TxTypeDelegatedRefundEscrow:  {Family: "ab", Note: "delegated status race; deadline wall clock"},
	types.TxTypeDelegatedDisputeEscrow: {Family: "a", Note: "delegated status race"},
	types.TxTypeEscrowCreateRealm:      {Family: "b", Note: "reads the wall clock"},
	types.TxTypeEscrowUpdateRealm:      {Family: "b", Note: "reads the wall clock"},
	types.TxTypeDelegatedCreateEscrow:  {Family: "ab", Note: "delegated create reads the wall clock"},
	types.TxTypeCreateLoyaltyBusiness:  {Note: "loyalty admin"},
	types.TxTypeLoyaltySetPaymaster:    {Note: "loyalty admin"},
	types.TxTypeLoyaltyAddMerchant:     {Note: "loyalty admin"},
	types.TxTypeLoyaltyRemoveMerchant:  {Note: "loyalty admin"},
	types.TxTypeCreateLoyaltyProgram:   {Note: "loyalty admin"},
	types.TxTypeUpdateLoyaltyProgram:   {Note: "loyalty admin"},
	types.TxTypePauseLoyaltyProgram:    {Note: "loyalty admin"},
	types.TxTypeResumeLoyaltyProgram:   {Note: "loyalty admin"},
	types.TxTypeSwapVoucherReverse:     {Senderless: true, Note: "voucher reversal"},
	types.TxTypeSwapMarkReconciled:     {Senderless: true, Note: "voucher reconciliation"},
	types.TxTypeSubmitEvidence:         {Senderless: true, Family: "h", Note: "no account, nonce or quota; expiry"},
}

// unknownTypeRows are type bytes that are NOT defined transaction types: the
// zero byte, the gap at 0x1F, the first free byte, and the maximum.
var unknownTypeRows = []types.TxType{0x00, 0x1F, 0x4D, 0xFF}

// TestPoisonMatrixHasRowForEveryTxType is the registry check: it iterates the
// full set of defined types and fails, naming the type, if any lacks a row.
func TestPoisonMatrixHasRowForEveryTxType(t *testing.T) {
	registered := make(map[types.TxType]struct{})
	for _, typ := range types.AllTxTypes() {
		registered[typ] = struct{}{}
		row, ok := poisonMatrixRows[typ]
		if !ok {
			t.Errorf("transaction type 0x%02X (%s) has no row in poisonMatrixRows: add one so it is put through the poison matrix", byte(typ), types.TxTypeName(typ))
			continue
		}
		if row.Senderless == types.RequiresSignature(typ) {
			t.Errorf("row for 0x%02X (%s) says Senderless=%v but types.RequiresSignature=%v", byte(typ), types.TxTypeName(typ), row.Senderless, types.RequiresSignature(typ))
		}
	}
	for typ := range poisonMatrixRows {
		if _, ok := registered[typ]; !ok {
			t.Errorf("poisonMatrixRows has a row for 0x%02X which is not a registered transaction type", byte(typ))
		}
	}
	for _, typ := range unknownTypeRows {
		if _, ok := registered[typ]; ok {
			t.Errorf("0x%02X is listed as an unknown type but is a registered transaction type", byte(typ))
		}
	}
}

// matrixTypes returns every type the matrix runs: all defined types followed by
// the unknown-byte rows.
func matrixTypes() []types.TxType {
	all := append([]types.TxType(nil), types.AllTxTypes()...)
	return append(all, unknownTypeRows...)
}

func matrixLabel(typ types.TxType) string {
	if _, defined := poisonMatrixRows[typ]; defined {
		return fmt.Sprintf("0x%02X_%s", byte(typ), types.TxTypeName(typ))
	}
	return fmt.Sprintf("0x%02X_unknown", byte(typ))
}

// TestPoisonMatrixCreateBlockSurvivesEveryTxType offers CreateBlock one
// hostile transaction of every type with every payload, next to one valid
// transfer, and requires: no error, no panic, the transfer in the block, and a
// block that validates. A recovered panic is a handler bug and fails the row
// even though containment worked, because a panic here means some transaction
// type can crash an unprotected validator.
func TestPoisonMatrixCreateBlockSurvivesEveryTxType(t *testing.T) {
	t.Setenv("NHB_ENV", "dev")
	payloads := livenessPayloads()

	var (
		mu        sync.Mutex
		slowest   time.Duration
		slowestAt string
	)
	// The subtests run in parallel (each has its own node and database), which
	// keeps a 79-type by 14-payload matrix -- every cell is a full block build
	// plus a full validation -- to a few seconds of wall time.
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		t.Logf("slowest CreateBlock across the matrix: %s (%s)", slowest, slowestAt)
	})
	for _, typ := range matrixTypes() {
		typ := typ
		t.Run(matrixLabel(typ), func(t *testing.T) {
			t.Parallel()
			node := newLivenessNode(t)
			honest := livenessKey(t)
			livenessFund(t, node, honest.PubKey().Address().Bytes(), 1_000_000_000, 0)
			for _, payload := range payloads {
				livenessResetStrikes(node)
				poisonKey := livenessKey(t)
				livenessFund(t, node, poisonKey.PubKey().Address().Bytes(), 1_000_000_000_000, 1_000_000_000_000)
				to := make([]byte, 20)
				to[19] = 0x02
				poison := livenessSign(t, poisonKey, typ, 0, payload.data, to, 0)
				transfer := livenessTransfer(t, honest, 0)
				panicsBefore := livenessPanicsRecovered(node)

				started := time.Now()
				block, err := node.CreateBlock([]*types.Transaction{poison, transfer})
				if took := time.Since(started); took > 0 {
					mu.Lock()
					if took > slowest {
						slowest, slowestAt = took, fmt.Sprintf("%s/%s", matrixLabel(typ), payload.name)
					}
					mu.Unlock()
				}
				if err != nil {
					t.Fatalf("payload %s: CreateBlock returned an error (whole-block abort): %v", payload.name, err)
				}
				if block == nil {
					t.Fatalf("payload %s: CreateBlock returned no block", payload.name)
				}
				if !livenessBlockContains(t, block, transfer) {
					t.Fatalf("payload %s: the valid transfer is missing from the block (%d transactions)", payload.name, len(block.Transactions))
				}
				if err := node.ValidateBlock(block); err != nil {
					t.Fatalf("payload %s: the block CreateBlock produced does not validate: %v", payload.name, err)
				}
				if got := livenessPanicsRecovered(node); got != panicsBefore {
					t.Fatalf("payload %s: an apply panic was recovered (%d): a handler for this type panics on this payload", payload.name, got-panicsBefore)
				}
			}
		})
	}
}

// TestPoisonMatrixMempoolPathReleasesInFlight runs a subset of the matrix
// through the real mempool cycle -- GetMempool, CreateBlock, CommitBlock -- to
// exercise release of in-flight marks: after commit nothing may be left
// leased, the valid transfer must be gone from the mempool, and the poison
// transaction is either gone (included or pruned) or still resident but not
// hidden.
func TestPoisonMatrixMempoolPathReleasesInFlight(t *testing.T) {
	t.Setenv("NHB_ENV", "dev")
	byName := map[string]livenessPayload{}
	for _, p := range livenessPayloads() {
		byName[p.name] = p
	}
	for _, typ := range matrixTypes() {
		typ := typ
		t.Run(matrixLabel(typ), func(t *testing.T) {
			t.Parallel()
			node := newLivenessNode(t)
			honest := livenessKey(t)
			livenessFund(t, node, honest.PubKey().Address().Bytes(), 1_000_000_000, 0)
			var honestNonce uint64
			for _, name := range []string{"empty", "garbage64", "json_obj"} {
				payload := byName[name]
				livenessResetStrikes(node)
				poisonKey := livenessKey(t)
				livenessFund(t, node, poisonKey.PubKey().Address().Bytes(), 1_000_000_000_000, 1_000_000_000_000)
				to := make([]byte, 20)
				to[19] = 0x02
				poison := livenessSign(t, poisonKey, typ, 0, payload.data, to, 0)
				transfer := livenessTransfer(t, honest, honestNonce)
				livenessInject(node, poison, transfer)

				offered := node.GetMempool()
				block, err := node.CreateBlock(offered)
				if err != nil {
					t.Fatalf("%s: CreateBlock: %v", name, err)
				}
				if !livenessBlockContains(t, block, transfer) {
					t.Fatalf("%s: transfer missing from block", name)
				}
				if err := node.CommitBlock(block); err != nil {
					t.Fatalf("%s: CommitBlock: %v", name, err)
				}
				honestNonce++

				if livenessResident(t, node, transfer) {
					t.Fatalf("%s: committed transfer is still in the mempool", name)
				}
				if n := livenessInFlight(node); n != 0 {
					t.Fatalf("%s: %d transactions left marked in flight after commit", name, n)
				}
				// Whatever became of the poison transaction, drop it so the next
				// payload starts from an empty mempool.
				node.dropTransactionsFromMempool([]*types.Transaction{poison})
			}
		})
	}
}
