package config

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"nhbchain/consensus"
	"nhbchain/core/genesis"
	"nhbchain/crypto"
	"nhbchain/native/fees"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
	"nhbchain/native/potso"
	swap "nhbchain/native/swap"

	"github.com/BurntSushi/toml"
)

var defaultAllowedGovernanceParams = []string{
	"fees.baseFee",
	governance.ParamKeyMinimumValidatorStake,
	governance.ParamKeyStakingAprBps,
	governance.ParamKeyStakingPayoutPeriodDays,
	governance.ParamKeyStakingUnbondingDays,
	governance.ParamKeyStakingMinStakeWei,
	governance.ParamKeyStakingMaxEmissionPerYearWei,
	governance.ParamKeyMintNHBMaxEmissionPerYearWei,
	governance.ParamKeyMintZNHBMaxEmissionPerYearWei,
	governance.ParamKeyStakingRewardAsset,
	governance.ParamKeyStakingCompoundDefault,
	governance.ParamKeyLoyaltyDynamicTargetBps,
	governance.ParamKeyLoyaltyDynamicMinBps,
	governance.ParamKeyLoyaltyDynamicMaxBps,
	governance.ParamKeyLoyaltyDynamicSmoothingStepBps,
	governance.ParamKeyLoyaltyDynamicCoverageMax,
	governance.ParamKeyLoyaltyDynamicCoverageLookbackDays,
	governance.ParamKeyLoyaltyDynamicDailyCapPctOf7dFees,
	governance.ParamKeyLoyaltyDynamicDailyCapUSD,
	governance.ParamKeyLoyaltyDynamicYearlyCapPctOfInitialSupply,
	governance.ParamKeyLoyaltyDynamicPricePair,
	governance.ParamKeyLoyaltyDynamicPriceTwapWindowSeconds,
	governance.ParamKeyLoyaltyDynamicPriceMaxAgeSeconds,
	governance.ParamKeyLoyaltyDynamicPriceMaxDeviationBps,
	governance.ParamKeyLoyaltyDynamicPriceGuardEnabled,
	"network.seeds",
	"potso.abuse.MaxUserShareBps",
	"potso.abuse.MinStakeToEarnWei",
	"potso.abuse.QuadraticTxDampenAfter",
	"potso.abuse.QuadraticTxDampenPower",
	"potso.rewards.EmissionPerEpochWei",
	"potso.weights.AlphaStakeBps",
	governance.ParamKeyMarketFlatFeeWei,
	governance.ParamKeyPaymasterTopUpFeeWei,
	// swap.PerAddressDailyCapWei/PerAddressMonthlyCapWei/PerTxMinWei/
	// PerTxMaxWei were removed from this list: they were never actually
	// wired to a reader (ParamStoreGet was never called with these exact
	// keys), so a param.update proposal setting them was a silent no-op.
	// The real, wired mechanism for the swap redeem direction's (swap-out
	// burn, TxTypeRedeemNHB) equivalent caps is now the dedicated
	// governance.ProposalKindSwapRiskParams proposal kind (key namespace:
	// swap.risk.redeem.*), which does not use this generic AllowedParams
	// allow-list at all. There is no mint-side (fiat-gateway voucher mint,
	// TxTypeSwapVoucherMint) equivalent at all -- those vouchers draw from a
	// fixed, pre-allocated genesis treasury Sale Pool rather than minting
	// new supply, so they carry no external financial risk needing a
	// circuit breaker. See core/swap_risk_params.go.
	// swap.VelocityWindowSeconds, swap.VelocityMaxMints, swap.cashOut.assetMonthlyCapWei,
	// lending.MaxLTVBps, lending.LiquidationThresholdBps, lending.ReserveFactorBps,
	// lending.ProtocolFeeBps and lending.breaker.MaxTotal{Supply,Borrow,Collateral}Wei
	// were removed from this list (PL-DC-10): the engine has no validator for any of them,
	// so a proposal for one was always rejected with "missing validation rule" and the list
	// promised voters a change the network cannot make. A test fails if a key without a
	// validator is added here again.
}

type Config struct {
	// UndecodedKeys lists the keys in the config file that this build does not know (a misspelling, or a setting from another version); they are ignored.
	// Filled by Load, never read from the file.
	UndecodedKeys              []string `toml:"-"`
	ListenAddress              string   `toml:"ListenAddress"`
	RPCAddress                 string   `toml:"RPCAddress"`
	RPCTrustedProxies          []string `toml:"RPCTrustedProxies"`
	RPCTrustProxyHeaders       bool     `toml:"RPCTrustProxyHeaders"`
	RPCAllowlistCIDRs          []string `toml:"RPCAllowlistCIDRs"`
	RPCWebSocketOrigins        []string `toml:"RPCWebSocketOrigins"`
	RPCWebSocketMaxConnections int      `toml:"RPCWebSocketMaxConnections"`
	RPCWebSocketMaxPerIP       int      `toml:"RPCWebSocketMaxPerIP"`
	// The public read queries that are not cheap (transaction and address
	// lookups the indexes cannot answer, explorer snapshots of a non-default
	// window, transaction-window statistics, the lending and market reads that
	// hold the state lock) share a small pool so they cannot take the CPU that
	// block production needs; see docs/ops/rpc-query-limits.md. Every field is
	// optional and zero selects its default, which needs no configuration:
	//   RPCQueryMaxConcurrent  queries running their heavy part at once
	//                          (half the CPUs, at least 1 and at most 2)
	//   RPCQueryMaxPerClient   in flight (running or waiting) per client (1)
	//   RPCQueryQueueDepth     queries allowed to wait for a slot (8)
	//   RPCQueryQueueWaitMS    how long one waits before it is refused (100)
	//   RPCQueryTimeoutSeconds bound on one such query, start to answer (10)
	// A query the pool refuses gets HTTP 429, code -32020 and a Retry-After
	// hint; one that runs out of time gets HTTP 503 and code -32021.
	//
	// RPCDisableExplorerLoop stops the background work that keeps the explorer
	// current -- the loop that rebuilds the default explorer snapshot after every
	// block and streams it to /ws/explorer, the advance of the all-time payment
	// index that goes with it, and the warm-up of the lending read index. Set it
	// on a node that serves no public RPC. The default (false) keeps them
	// running. With it set, a snapshot that is asked for is built on demand and
	// cached per window and chain height, and /ws/explorer refuses connections.
	RPCDisableExplorerLoop   bool                         `toml:"RPCDisableExplorerLoop"`
	RPCQueryMaxConcurrent    int                          `toml:"RPCQueryMaxConcurrent"`
	RPCQueryMaxPerClient     int                          `toml:"RPCQueryMaxPerClient"`
	RPCQueryQueueDepth       int                          `toml:"RPCQueryQueueDepth"`
	RPCQueryQueueWaitMS      int                          `toml:"RPCQueryQueueWaitMS"`
	RPCQueryTimeoutSeconds   int                          `toml:"RPCQueryTimeoutSeconds"`
	RPCProxyHeaders          RPCProxyHeaders              `toml:"RPCProxyHeaders"`
	RPCJWT                   RPCJWT                       `toml:"RPCJWT"`
	RPCSwapAuth              RPCSwapAuth                  `toml:"RPCSwapAuth"`
	RPCReadHeaderTimeout     int                          `toml:"RPCReadHeaderTimeout"`
	RPCReadTimeout           int                          `toml:"RPCReadTimeout"`
	RPCWriteTimeout          int                          `toml:"RPCWriteTimeout"`
	RPCIdleTimeout           int                          `toml:"RPCIdleTimeout"`
	RPCMaxTxPerWindow        int                          `toml:"RPCMaxTxPerWindow"`
	RPCMaxTxPerIP            int                          `toml:"RPCMaxTxPerIP"`
	RPCMaxTxPerIdentity      int                          `toml:"RPCMaxTxPerIdentity"`
	RPCMaxTxPerChain         int                          `toml:"RPCMaxTxPerChain"`
	RPCMaxTxPerIdentityChain int                          `toml:"RPCMaxTxPerIdentityChain"`
	RPCRouteRateLimits       map[string]RPCRouteRateLimit `toml:"RPCRouteRateLimits"`
	// RPCMethodCosts weights RPC methods in the rate limiters: a call takes this many
	// tokens instead of one. Empty by default (every call costs one). Node-local.
	RPCMethodCosts              map[string]int `toml:"RPCMethodCosts"`
	RPCRateLimitWindow          int            `toml:"RPCRateLimitWindow"`
	RPCCallerMetadataMaxTTL     int            `toml:"RPCCallerMetadataMaxTTL"`
	RPCAllowInsecure            bool           `toml:"RPCAllowInsecure"`
	RPCAllowInsecureUnspecified bool           `toml:"RPCAllowInsecureUnspecified"`
	RPCTLSCertFile              string         `toml:"RPCTLSCertFile"`
	RPCTLSKeyFile               string         `toml:"RPCTLSKeyFile"`
	RPCTLSClientCAFile          string         `toml:"RPCTLSClientCAFile"`
	DataDir                     string         `toml:"DataDir"`
	GenesisFile                 string         `toml:"GenesisFile"`
	AllowAutogenesis            bool           `toml:"AllowAutogenesis"`
	// QuorumCertActivationHeight enables NHB-TRIAGE-C1's quorum-certificate
	// check on the untrusted P2P block-sync path once set: blocks at this
	// height or below sync without one (grandfathering already-committed
	// history); blocks above it must carry a QuorumCert that verifies
	// against the current validator set, or they're rejected. 0 (the
	// default/unset value) leaves the check fully disabled -- this field
	// must be explicitly set to the chain's current tip height, identically
	// on every validator, as one coordinated deploy (see
	// core.Node.SetQuorumCertActivationHeight's doc comment). Note: because
	// 0 means "disabled" here, there is no way to express "enforce from
	// height 1" via this config field specifically -- that only matters for
	// a brand-new chain wanting the protection from genesis, not for
	// upgrading an already-live one. This field only gates the CERTIFICATE
	// CHECK; it does not gate the quorum bar itself. The bar a certificate (or
	// an ordinary vote tally) must clear -- strictly more than two thirds of
	// voting power -- has no gate at all and has been unconditional since the
	// DC-01 safety fix: any value set here, on a fresh replay or a live
	// upgrade alike, must be at or above the first height actually committed
	// under a binary that already enforces that strict bar, or a node
	// replaying from genesis with an older, looser-quorum binary in its
	// history could compute a different chain than one that never ran it.
	QuorumCertActivationHeight uint64 `toml:"QuorumCertActivationHeight"`
	// RewardsByStakeFromEpoch is the first epoch whose emission is paid by stake: the validators' and the engagement pool follow each validator's stake,
	// like the stakers' pool already does, and every pool is credited to the wallets that locked that stake instead of to the validator's own account (the
	// key on its server). 0 (the default/unset value) keeps every earlier release's split: equal per validator and by engagement score, to the validator's
	// own account. This is a consensus value: it must be the same on every validator, and on every node that replays blocks from a snapshot, and it must be
	// in force on all of them before the epoch it names begins (see core.StateProcessor.SetRewardsByStakeFromEpoch). Change it only as one coordinated
	// restart of every validator, with the epoch far enough ahead to finish it.
	RewardsByStakeFromEpoch uint64          `toml:"RewardsByStakeFromEpoch"`
	ValidatorKeystorePath   string          `toml:"ValidatorKeystorePath"`
	ValidatorKMSURI         string          `toml:"ValidatorKMSURI"`
	ValidatorKMSEnv         string          `toml:"ValidatorKMSEnv"`
	NetworkName             string          `toml:"NetworkName"`
	Bootnodes               []string        `toml:"Bootnodes"`
	PersistentPeers         []string        `toml:"PersistentPeers"`
	BootstrapPeers          []string        `toml:"BootstrapPeers,omitempty"`
	MaxPeers                int             `toml:"MaxPeers"`
	MaxInbound              int             `toml:"MaxInbound"`
	MaxOutbound             int             `toml:"MaxOutbound"`
	MinPeers                int             `toml:"MinPeers"`
	OutboundPeers           int             `toml:"OutboundPeers"`
	PeerBanSeconds          int             `toml:"PeerBanSeconds"`
	ReadTimeout             int             `toml:"ReadTimeout"`
	WriteTimeout            int             `toml:"WriteTimeout"`
	MaxMsgBytes             int             `toml:"MaxMsgBytes"`
	MaxMsgsPerSecond        float64         `toml:"MaxMsgsPerSecond"`
	ClientVersion           string          `toml:"ClientVersion"`
	P2P                     P2PSection      `toml:"p2p"`
	Potso                   PotsoConfig     `toml:"potso"`
	Governance              GovConfig       `toml:"governance"`
	Swap                    swap.Config     `toml:"swap"`
	Lending                 lending.Config  `toml:"lending"`
	Bridge                  BridgeConfig    `toml:"bridge"`
	Amm                     AmmConfig       `toml:"amm"`
	Subscriptions           Subscriptions   `toml:"subscriptions"`
	Mempool                 MempoolConfig   `toml:"mempool"`
	Global                  Global          `toml:"global"`
	Consensus               Consensus       `toml:"consensus"`
	NetworkSecurity         NetworkSecurity `toml:"network_security"`

	// POSSweepSkipActivationHeight is the first block height at which the
	// end-of-block sweep of expired POS authorizations carries on past an
	// expired hold whose payer's locked balance no longer covers it, leaves
	// that hold pending, and releases the holds queued behind it. Before this
	// height (and at every height when the value is 0, the default) the sweep
	// ends at that hold and the holds behind it stay pending. The two
	// behaviours leave different state, so this is a consensus setting: it
	// MUST be identical on every validator, and it must only be changed with
	// every validator restarted together, with the height chosen above the
	// chain tip at that moment. See docs/specs/pos-lifecycle.md.
	POSSweepSkipActivationHeight uint64 `toml:"POSSweepSkipActivationHeight"`
	// POSHoldUnstakeGuardActivationHeight is the first block height from which
	// an unstake may not unbond ZNHB that backs pending POS holds. A POS hold
	// and a delegation both sit in the account's locked ZNHB, and without this
	// rule an unstake can take more than was delegated, leaving a hold its
	// payer cannot cover. 0 (the default) leaves the rule off. Unlike a node
	// setting it is a chain rule: set the same non-zero height on every
	// validator, above the chain tip and before the chain reaches it, or
	// validators disagree on any block that carries an unstake the rule
	// refuses. As a top-level key it goes before the first [table] of the
	// file. See core.StateProcessor.SetPOSHoldUnstakeGuardActivationHeight.
	POSHoldUnstakeGuardActivationHeight uint64 `toml:"POSHoldUnstakeGuardActivationHeight"`
	// GovCreditPreservationActivationHeight enables PL-R1-GOVREFUND's fix
	// once set: TxTypeGovFinalize/TxTypeGovExecute transactions below this
	// height keep persisting the pre-engine-call sender snapshot
	// after their nonce bump (grandfathering already-committed history that
	// may rely on today's behavior); transactions at or above it reload the
	// sender's account fresh first, so a same-address credit
	// engine.Finalize/Execute just wrote (a deposit refund, a
	// forfeited-deposit sweep, or a treasury-directive payout) survives
	// instead of being silently clobbered. 0 (the default/unset value)
	// leaves the fix fully disabled -- this field must be explicitly set to
	// the chain's current tip height, identically on every validator, as
	// one coordinated deploy (see core.Node.SetGovCreditPreservationActivationHeight's
	// doc comment and core.StateProcessor.SetGovCreditPreservationActivationHeight's
	// doc comment in core/governance_tx.go for the full rationale, including
	// why this ships gated rather than unconditional).
	GovCreditPreservationActivationHeight uint64 `toml:"GovCreditPreservationActivationHeight"`
	// FeeRouteAliasActivationHeight is the first block height from which an NHB
	// domain fee whose route wallet is the transfer's own sender or recipient is
	// credited to that account as the transfer leaves it, instead of to a
	// separately loaded copy the transfer's own later write overwrites, which
	// destroys the fee (PL-R1-FEEROUTE). The same holds for the share of that fee
	// that funds the buyback accrual account when the accrual account is the
	// transfer's sender or recipient (PL-R5T-59). The same fix for ZNHB is already in
	// force at every height. 0 (the default) leaves the NHB half off. A chain
	// rule, not a node setting: it changes the state a block leaves behind, so
	// set the same non-zero height on every validator, above the chain tip, with
	// all of them restarted together, or they disagree on every block that
	// routes such a fee. As a top-level key it goes before the first [table] of
	// the file. See core.StateProcessor.SetFeeRouteAliasActivationHeight.
	FeeRouteAliasActivationHeight uint64 `toml:"FeeRouteAliasActivationHeight"`
	// LendingFixedTermIDActivationHeight is the first block height from which a
	// fixed-term loan or deposit is stored under an ID that includes its owner,
	// and an origination whose ID already has a record is refused (PL-R5T-02).
	// Until then the ID is the transaction hash, which covers no sender, so two
	// accounts signing field-identical transactions share one ID and the second
	// record overwrites the first while both moves of cash and both pool totals
	// stand. 0 (the default) keeps the transaction hash as the ID. A chain rule,
	// not a node setting: set the same non-zero height on every validator, above
	// the chain tip, with all of them restarted together. As a top-level key it
	// goes before the first [table] of the file. See
	// core.StateProcessor.SetLendingFixedTermIDActivationHeight.
	LendingFixedTermIDActivationHeight uint64 `toml:"LendingFixedTermIDActivationHeight"`
	// NHBSupplyGenesisCorrectionActivationHeight is the first block height at
	// which the tracked NHB supply counter (nhb_getTotalSupply) is raised, once,
	// by the part of the genesis allocation it never counted (PL-R1-SUPPLY90K):
	// the live network's counter started 90,000 NHB short of its 100,000 NHB
	// allocation and has stayed exactly that far short, and it refuses a burn
	// larger than itself, so NHB redemptions and swap settlements above its figure fail.
	// 0 (the default) leaves the counter as it is. No balance changes and no NHB
	// is created. A chain rule, not a node setting: set the same non-zero height
	// on every validator, above the chain tip, with all of them restarted
	// together. As a top-level key it goes before the first [table] of the file.
	// See core.StateProcessor.ReconcileNHBGenesisShortfallOnce.
	NHBSupplyGenesisCorrectionActivationHeight uint64 `toml:"NHBSupplyGenesisCorrectionActivationHeight"`
	// TransferMonthlyFreeTierActivationHeight is the first block height from
	// which a sender's first TransferMonthlyFreeTierTxPerMonth plain transfers
	// in each UTC calendar month, in NHB or ZNHB, pay no transfer fee (PL-CLM-02).
	// Until then the only waiver is the spend allowance (TransferFreeTierSpendWei,
	// a lifetime amount); the statement "your first 100 transactions each month
	// are free" describes the merchant point-of-sale fee domain, not ordinary
	// transfers. The tier adds to the allowance and never removes it. 0 (the
	// default) leaves transfers as they are, and the tier also needs a non-zero
	// TransferMonthlyFreeTierTxPerMonth. A chain rule, not a node setting: set
	// the same values on every validator, above the chain tip, with all of them
	// restarted together. As top-level keys they go before the first [table] of
	// the file. See core.StateProcessor.SetTransferMonthlyFreeTier.
	TransferMonthlyFreeTierActivationHeight uint64 `toml:"TransferMonthlyFreeTierActivationHeight"`
	// TransferMonthlyFreeTierTxPerMonth is the number of plain transfers per
	// sender per UTC calendar month the tier waives the fee for. 0 (the default)
	// leaves the tier off whatever the height.
	TransferMonthlyFreeTierTxPerMonth uint64 `toml:"TransferMonthlyFreeTierTxPerMonth"`
	// LoyaltyBudgetFloorActivationHeight is the first block height from which the
	// daily budget for base rewards (the reward for spending NHB) is at least
	// LoyaltyBudgetFloorZNHBWei (PL-CLM-02). The budget is the smaller of a USD
	// cap and 60 percent of the last seven days of fees, so with next to no fees
	// it is zero and every reward is pro-rated to nothing; the floor is the
	// day's emission the loyalty treasury is willing to fund regardless of fees,
	// and it is limited to the existing daily USD cap (and not applied when that
	// cap is set and no price is available). The per-transaction, per-user and
	// per-counterparty caps still apply. 0 (the default), or an empty amount,
	// leaves the budget as it is. How large the floor is, and whether rewards are
	// funded this way at all, is an owner decision: it ships off. A chain rule:
	// set the same values on every validator, above the chain tip, with all of
	// them restarted together. See core.StateProcessor.SetLoyaltyBudgetFloor.
	LoyaltyBudgetFloorActivationHeight uint64 `toml:"LoyaltyBudgetFloorActivationHeight"`
	// LoyaltyBudgetFloorZNHBWei is the floor, in ZNHB wei, as a decimal string
	// (the same notations as the other amounts, for example "1000e18").
	LoyaltyBudgetFloorZNHBWei string `toml:"LoyaltyBudgetFloorZNHBWei"`
	// PotsoGovernanceWeightActivationHeight is the first block height from which
	// the weight snapshot that decides who may vote on a proposal, and with how
	// much power, is built separately from the one that pays POTSO rewards
	// (PL-CLM-04). Until then only the validators hold weight: a validator's
	// counted stake includes everything delegated to it, so the owners of the
	// delegated ZNHB hold none. From the height, in the governance snapshot only:
	// delegated stake is counted for the delegator instead of the validator (the
	// total does not change), and a snapshot is built every epoch even when the
	// reward treasury is empty (today an epoch with no reward budget stores none
	// and every vote fails). Rewards are paid from the existing snapshot and do not
	// change. Engagement is not changed (see the comment in
	// core/potso_governance_weights.go). 0 (the default) leaves governance reading
	// the snapshot of the rewards. A chain rule: set the same value on every
	// validator, above the chain tip, with all of them restarted together. See
	// core.StateProcessor.SetPotsoGovernanceWeights.
	PotsoGovernanceWeightActivationHeight uint64 `toml:"PotsoGovernanceWeightActivationHeight"`
	// ValidatorFlagNonceActivationHeight is the first block height from which a
	// stake transaction that only registers the sender as a validator candidate, or an
	// unstake transaction that only deregisters it, each carrying no value, consumes
	// the sender's nonce like every other transaction (PL-R5T-91). Until then those two
	// never advance the nonce, so the signed bytes of one of them pass the strict nonce
	// check again and again and re-apply the flag state the key owner signed at that
	// nonce. 0 (the default) leaves both as they are at every height. A chain rule: set
	// the same value on every validator, above the chain tip, with all of them
	// restarted together. See core.StateProcessor.SetValidatorFlagNonceActivationHeight.
	ValidatorFlagNonceActivationHeight uint64 `toml:"ValidatorFlagNonceActivationHeight"`
	// MintRunningChainIDActivationHeight is the first block height from which a mint
	// voucher is also accepted when its chainId is the running chain's own id (the
	// genesis-derived id the swap voucher path already uses), as well as the retired
	// Phase E id core.MintChainID that is the only one accepted today (PL-R5T-58).
	// Every mint committed so far named the Phase E id, so accepting both is replay-safe;
	// the Phase E id stays valid until MintPhaseEChainIDRetireHeight. 0 (the default)
	// leaves the check as it is at every height. A chain rule: set the same value on every
	// validator, above the chain tip, with all of them restarted together. See
	// core.StateProcessor.SetMintRunningChainIDActivationHeight.
	MintRunningChainIDActivationHeight uint64 `toml:"MintRunningChainIDActivationHeight"`
	// MintPhaseEChainIDRetireHeight is the first block height from which a mint voucher
	// that names the retired Phase E chain id (core.MintChainID) is refused and only the
	// running chain's own id is accepted (PL-R5T-58). It also switches the running-id
	// acceptance on from that height when MintRunningChainIDActivationHeight is 0 or
	// later. Whoever signs vouchers must have moved to the running id before the chain
	// reaches it. 0 (the default) never retires the Phase E id. A chain rule: set the
	// same value on every validator, above the chain tip, with all of them restarted
	// together. See core.StateProcessor.SetMintPhaseEChainIDRetireHeight.
	MintPhaseEChainIDRetireHeight uint64 `toml:"MintPhaseEChainIDRetireHeight"`
	// TxNetworkChainIDActivationHeight is the first block height from which a
	// transaction must carry the network's own chain id (the genesis-derived id the node
	// reports, 18346390202490284624 on the live network) and the legacy constant
	// 0x4e4842 that every NHB network shares is refused (PL-R5T-29). The id is part of
	// the signed hash, so a transaction signed for one network no longer verifies on
	// another. Until the height the legacy constant is the only id accepted, as in every
	// committed block. Every signer (command line, SDKs, the portal, the off-chain
	// submitters) must sign with the network id before the chain reaches the height:
	// nhb_getNetworkStats reports the id a transaction must carry now (txChainId). 0 (the default)
	// leaves the check as it is at every height. A chain rule: set the same value on
	// every validator, above the chain tip, with all of them restarted together. See
	// core.StateProcessor.SetTxNetworkChainIDActivationHeight.
	TxNetworkChainIDActivationHeight uint64 `toml:"TxNetworkChainIDActivationHeight"`
	// VoteChainBindingActivationHeight is the first block height from which a consensus
	// vote is signed over the network's genesis hash as well as its block hash, round,
	// type and height (PL-R5T-31). It applies to the votes a validator signs and checks, to
	// the quorum certificate that a synced block carries and to the two votes of an
	// equivocation proof: a vote at or above the height verifies only against the bound
	// digest, a vote below it only against the legacy one, so history and a fresh node's
	// replay are unchanged. Without it two votes cast by one key on two networks at the
	// same height, round and type form a valid equivocation proof that forfeits the
	// validator's stake. 0 (the default) leaves the legacy digest at every height. A chain
	// rule: set the same value on every validator, above the chain tip, with all of them
	// restarted together. See core.StateProcessor.SetVoteChainBindingActivationHeight.
	VoteChainBindingActivationHeight uint64 `toml:"VoteChainBindingActivationHeight"`
	// SlashEligibilityRefreshActivationHeight is the first block height from which a
	// validator slashed for equivocation has its eligibility entry and its place in
	// the validator set re-evaluated in the block that slashes it (PL-R5T-25). Until
	// then the slasher debits the offender's stake without going through the one place
	// those are recomputed, so the offender keeps the voting power, the eligibility
	// basis and the reward and governance weight it had until its account is next
	// written. 0 (the default) leaves that as it is. A chain rule: set the same value
	// on every validator, above the chain tip, with all of them restarted together. See
	// core.ValidatorRules.
	SlashEligibilityRefreshActivationHeight uint64 `toml:"SlashEligibilityRefreshActivationHeight"`
	// SlashPendingUnbondsActivationHeight is the first block height from which a proven
	// equivocation also forfeits the offender's own unclaimed unbonds, after its locked
	// balance, and the slash is sized on its stake plus those unbonds (PL-R5T-26). Until
	// then ZNHB a validator moved into an unbond before the report landed (an unbond
	// lasts 7 days by default, the evidence window is 8,640 blocks) is out of reach and
	// the offense is still marked penalised with a slash of zero. Stake other wallets
	// delegated to the validator is not reached by this setting. 0 (the default) leaves
	// that as it is. A chain rule: set the same value on every validator, above the
	// chain tip, with all of them restarted together. See core.ValidatorRules.
	SlashPendingUnbondsActivationHeight uint64 `toml:"SlashPendingUnbondsActivationHeight"`
	// EvidenceIntakeGuardActivationHeight is the first block height from which the
	// evidence intake refuses the reports that fill its 256 slots without a proof
	// behind them (PL-R5T-09): a DOWNTIME or INVALID_BLOCK_PROPOSAL report (it needs only
	// the reporter's signature and does nothing when processed), an EQUIVOCATION report
	// whose offender holds less than the minimum validator stake in staking stake (plus
	// its own pending unbonds), and a report whose reporter's staking stake, not its
	// locked ZNHB, is under the minimum (a point-of-sale hold raises locked ZNHB and can
	// be voided at once). 0 (the default) leaves the intake as it is. A chain rule: set
	// the same value on every validator, above the chain tip, with all of them restarted
	// together. See core.ValidatorRules.
	EvidenceIntakeGuardActivationHeight uint64 `toml:"EvidenceIntakeGuardActivationHeight"`
	// StakeAPRFromStateActivationHeight is the first block height from which the reward
	// rate used when a stake, unstake or claim executes is the on-chain staking.aprBps
	// (the fixed default 1250 when none is stored or it cannot be read), read at
	// execution, and never the value this node cached at start-up from its own
	// config.toml (PL-R5T-27). Until then the cached value is used and two validators
	// with different config, or one restarted after a governed change, compute different
	// state for the same transaction. From the height a change of the parameter is
	// settled at the old rate in the block that executes it. 0 (the default) leaves the
	// cached rate in use. A chain rule: set the same value on every validator, above the
	// chain tip, with all of them restarted together. See core.ValidatorRules.
	StakeAPRFromStateActivationHeight uint64 `toml:"StakeAPRFromStateActivationHeight"`
	// ValidatorAdmissionGuardActivationHeight is the first block height from which a
	// validator that is new to the set that votes is admitted at an epoch boundary only
	// while the established validators (those that have been in the set for the whole
	// ValidatorAdmissionProbationSeconds, and those in it when the guard starts) keep
	// strictly more than two thirds of the power of the established validators plus the
	// newcomers (PL-VAL-01). Without it any holder of the minimum validator stake can
	// join, and when the incumbents sit close to a third each their quorum then needs the
	// newcomer: if it is switched off no block can be finished. Nothing is ever removed
	// from the set by the rule. 0 (the default) leaves admission as it is. A chain rule:
	// set the same value on every validator, above the chain tip, with all of them
	// restarted together. Needs ValidatorAdmissionProbationSeconds above zero. See
	// core.ValidatorRules.
	ValidatorAdmissionGuardActivationHeight uint64 `toml:"ValidatorAdmissionGuardActivationHeight"`
	// ValidatorAdmissionProbationSeconds is how long a validator must have been in the
	// set before it counts as established for the admission guard (PL-VAL-01). Zero
	// leaves the guard without effect. A chain rule: the same value on every validator.
	// See ValidatorAdmissionGuardActivationHeight and core.ValidatorRules.
	ValidatorAdmissionProbationSeconds uint64 `toml:"ValidatorAdmissionProbationSeconds"`
	// PotsoTreasuryShortfallActivationHeight is the first block height from which a POTSO
	// reward epoch that the reward treasury cannot cover is detected before anything is
	// written and degrades to a recorded zero payout (PL-DC-29): the budget is the
	// emission, no longer capped at the treasury balance first, so the shortfall check can
	// fire; the epoch is marked processed and never retried; the meta carries a Shortfall
	// flag and a potso.reward.shortfall event is emitted. Until then an underfunded
	// treasury quietly pays a smaller, capped epoch. The claim-mode half of PL-DC-29 is
	// not changed (there is no claim path). 0 (the default) leaves the capped payout. A
	// chain rule: set the same value on every validator, above the chain tip, with all of
	// them restarted together. See core.ValidatorRules.
	PotsoTreasuryShortfallActivationHeight uint64 `toml:"PotsoTreasuryShortfallActivationHeight"`
	// DelegatorHoldWeightActivationHeight is the first block height from which a
	// delegator's share of a validator's staker pool is weighed by its locked ZNHB
	// less the ZNHB its own pending POS authorizations hold (PL-EXT-25). A pending
	// hold moves ZNHB into the payer's locked balance, and that balance was the whole
	// weight, so a wallet could delegate a little, lock a large amount in a hold and
	// take a share of the epoch's emission far above what it staked. The hold is
	// refundable, so the weight it bought cost the attacker nothing. 0 (the default)
	// keeps the weight at the whole locked balance. A chain rule: set the same value
	// on every validator, above the chain tip, with all of them restarted together.
	// See core.ValidatorRules.
	DelegatorHoldWeightActivationHeight uint64 `toml:"DelegatorHoldWeightActivationHeight"`
	// PotsoAbuseGuardsActivationHeight is the first block height from which the five POTSO
	// abuse-guard values of this node's file are applied as configured (PL-EXT-39, PL-PO-01):
	// [potso.weights] MinStakeToWinWei and MinEngagementToWin, [potso.abuse]
	// MinStakeToEarnWei, QuadraticTxDampenAfter and MaxUserShareBps. An account with no
	// stake that makes two one-wei transfers an epoch (a transfer costs a percentage of its
	// value, so that costs nothing) reaches the engagement cap and takes up to 30 percent of
	// the epoch's emission and of the governance weight; these guards stop it, and all five
	// are 0 on the live validators. They were read at start-up with no height, so turning one
	// on would have been a chain rule at a restart. Before this height they are applied as 0
	// whatever the file says, so the values can be set on every validator ahead of it. 0 (the
	// default) = never. A chain rule: set the same height and the same values on every
	// validator, above the chain tip, with all of them restarted together. See
	// core.ValidatorRules.
	PotsoAbuseGuardsActivationHeight uint64 `toml:"PotsoAbuseGuardsActivationHeight"`
	// HeartbeatTimestampBoundActivationHeight is the first block height from which a heartbeat
	// transaction (TxTypeHeartbeat) whose payload carries a timestamp more than two minutes
	// after the block's own time is refused (PL-EXT-71). Until then any signed timestamp was
	// accepted, and one dated far in the future made every validator walk the engagement
	// rollover one day at a time, with an event for each day, up to millions of days. A
	// timestamp of 0 still means the block time and is never refused. 0 (the default) =
	// never. A chain rule: set the same height on every validator, above the chain tip, with
	// all of them restarted together. See core.ValidatorRules.
	HeartbeatTimestampBoundActivationHeight uint64 `toml:"HeartbeatTimestampBoundActivationHeight"`
	// LendingClosedDayClampActivationHeight is the first block height from which the next
	// cycle of a fixed-term loan's auto-debit or of a fixed-term deposit's payout is put on
	// the day of the block that schedules it when its own due day has already passed
	// (PL-EXT-83). The settlement scans the days after the one it last closed up to today
	// and never goes back, so a cycle put on a day it had closed was never attempted again:
	// the last installment of a loan was not collected and the last payout of a deposit,
	// principal included, was never made. 0 (the default) = never. A chain rule: set the
	// same height on every validator, above the chain tip, with all of them restarted
	// together. See core.ValidatorRules.
	LendingClosedDayClampActivationHeight uint64 `toml:"LendingClosedDayClampActivationHeight"`
	// EscrowEnvelopeChainBindingActivationHeight is the first block height from which
	// the signed payload of a delegated escrow create, release, refund or dispute, and
	// the decision payload of an arbitration quorum, must carry the network's own chain
	// id as "chainId" (a decimal string, the id nhb_getNetworkStats reports as
	// networkChainId) (PL-EXT-07). Their signatures bind the escrow id and the action
	// only, and the id is a hash of payer, payee, meta hash and nonce, so a signature
	// made for one network released or created the mirror escrow on any other network
	// that held it. 0 (the default) accepts the payloads as they are. A chain rule: set
	// the same value on every validator, above the chain tip, with all of them
	// restarted together, and only after the wallets and relayers that build these
	// payloads include the chain id. See core.ValidatorRules.
	EscrowEnvelopeChainBindingActivationHeight uint64 `toml:"EscrowEnvelopeChainBindingActivationHeight"`
	// LendingAccountingFixesActivationHeight is the first block height from which the
	// lending engine applies its corrected accounting rules. At present: a fixed-term
	// borrow accrues the market's interest first, like every other lending entry point,
	// instead of adding its principal to the pool's borrowed total while the accrual
	// stamp still dates from before the idle window, so that the next accrual charges the
	// whole window at a utilisation that already includes the loan (PL-R5T-50); and a
	// borrow that carries a developer fee no longer also counts that fee as
	// withdrawable, which would pay it a second time out of the shared pool balance
	// and the suppliers' total (PL-R5T-52); and a lending position still recorded on
	// a plain account (collateral, supply shares, debt) is taken by the default pool
	// only, and cleared from the plain account when taken, instead of being copied
	// into every pool that touches the account and paid out again from the shared
	// collateral vault (PL-EXT-16; the per-pool sweep of every account of PL-R5T-13
	// goes with it). 0 (the default) keeps the behaviour of every earlier release at
	// every height. A chain rule:
	// it decides what a lending transaction leaves behind, so set the same non-zero height
	// on every validator, above the chain tip, with all of them restarted together. As a
	// top-level key it goes before the first [table] of the file. See
	// core.StateProcessor.SetLendingAccountingFixesActivationHeight.
	LendingAccountingFixesActivationHeight uint64 `toml:"LendingAccountingFixesActivationHeight"`
	// LendingPoolLimitsActivationHeight is the first block height from which creating a
	// lending pool is bounded (PL-R5T-13). Until then pool creation is free and
	// unbounded: TxTypeLendingCreatePool needs no pool-count or id-length limit and
	// ignores the lending pause, and any lending transaction that names a pool the chain
	// has not seen creates it. From the height: a pool other than the default one exists
	// only if TxTypeLendingCreatePool created it (naming an unseen pool in any other
	// lending transaction is refused, without any sweep of accounts); a pool id is made
	// of letters, digits, '.', '_' and '-' only, at most LendingPoolIDMaxLength long
	// (0 = no length limit); at most LendingMaxPools pools exist (0 = no limit); and
	// creating a pool is refused while the lending module is paused. The two sizes are
	// owner decisions. 0 (the default) keeps the behaviour of every earlier release at
	// every height. A chain rule: set the same non-zero height, and the same sizes, on
	// every validator, above the chain tip, with all of them restarted together. As a
	// top-level key it goes before the first [table] of the file. See
	// core.StateProcessor.SetLendingPoolLimitsActivationHeight.
	LendingPoolLimitsActivationHeight uint64 `toml:"LendingPoolLimitsActivationHeight"`
	// LendingMaxPools is the most lending pools that may exist once
	// LendingPoolLimitsActivationHeight has been reached; 0 means no limit. The size is an
	// owner decision. A chain rule: the same value on every validator. As a top-level key
	// it goes before the first [table] of the file. See core.StateProcessor.SetLendingMaxPools.
	LendingMaxPools uint64 `toml:"LendingMaxPools"`
	// LendingPoolIDMaxLength is the longest lending pool id that may be created once
	// LendingPoolLimitsActivationHeight has been reached; 0 means no limit. The size is an
	// owner decision. A chain rule: the same value on every validator. As a top-level key
	// it goes before the first [table] of the file. See core.StateProcessor.SetLendingPoolIDMaxLength.
	LendingPoolIDMaxLength uint64 `toml:"LendingPoolIDMaxLength"`
	// LendingRefPriceGuardsActivationHeight is the first block height from which a lending
	// reference-price bundle is checked against the block that carries it (PL-EXT-13,
	// PL-R5T-53). Until then it needs only a valid signer quorum and a timestamp newer than
	// the last accepted one: one validly signed bundle dated far ahead rejects every honest
	// bundle after it (a timestamp above the largest signed 64-bit number also wraps and can
	// never be passed again), a signed bundle that never landed can be applied much later and
	// is stamped fresh, and a rate so small that its 1e18-scaled median is zero switches the
	// valuation of collateral to 1:1. From the height a bundle is refused when its timestamp
	// does not fit a signed 64-bit number, is more than LendingRefPriceMaxAheadSeconds after
	// the block time (0 = no such check), is more than LendingRefPriceMaxAgeSeconds before it
	// (0 = no such check), or has a median of zero. The two sizes are owner decisions. 0 (the
	// default) keeps the behaviour of every earlier release at every height. A chain rule: set
	// the same non-zero height, and the same sizes, on every validator, above the chain tip,
	// with all of them restarted together. As a top-level key it goes before the first [table]
	// of the file. See core.StateProcessor.SetLendingRefPriceGuardsActivationHeight.
	LendingRefPriceGuardsActivationHeight uint64 `toml:"LendingRefPriceGuardsActivationHeight"`
	// LendingRefPriceMaxAheadSeconds is how far after the block time a lending reference-price
	// bundle may be dated once LendingRefPriceGuardsActivationHeight has been reached; 0 means
	// no such check. The size is an owner decision. A chain rule: the same value on every
	// validator. As a top-level key it goes before the first [table] of the file. See
	// core.StateProcessor.SetLendingRefPriceMaxAheadSeconds.
	LendingRefPriceMaxAheadSeconds uint64 `toml:"LendingRefPriceMaxAheadSeconds"`
	// LendingRefPriceMaxAgeSeconds is how long before the block time a lending reference-price
	// bundle may be dated once LendingRefPriceGuardsActivationHeight has been reached; 0 means
	// no such check. The size is an owner decision. A chain rule: the same value on every
	// validator. As a top-level key it goes before the first [table] of the file. See
	// core.StateProcessor.SetLendingRefPriceMaxAgeSeconds.
	LendingRefPriceMaxAgeSeconds uint64 `toml:"LendingRefPriceMaxAgeSeconds"`
	// LendingRiskActivationHeight is the first block height from which the lending engine
	// applies the risk limits that the node's [lending] configuration names but the engine
	// never received (PL-DC-19): the liquidation bonus, the borrow caps (per block, total,
	// utilisation), the per-action pauses, the circuit breaker, and the supply and collateral
	// caps of [lending.breaker]. Until then they are zero in the engine whatever the file
	// says: no liquidator bonus, no cap, no pause. The limits are set in [lending.risk] and
	// [lending.breaker]; their sizes are owner decisions. 0 (the default) keeps the behaviour
	// of every earlier release at every height. A chain rule: set the same non-zero height,
	// and the same limits, on every validator, above the chain tip, with all of them
	// restarted together. As a top-level key it goes before the first [table] of the file.
	// See core.StateProcessor.SetLendingRiskActivationHeight.
	LendingRiskActivationHeight uint64 `toml:"LendingRiskActivationHeight"`
	// LendingFeeWithdrawActivationHeight is the first block height from which
	// TxTypeLendingWithdrawProtocolFees (0x4D) is a valid transaction (PL-DC-19). The lending
	// engine accrues a protocol share of the interest (the reserve factor) in a counter that no
	// transaction could ever pay out. From the height an account holding ROLE_LENDING_PROTOCOL_ADMIN
	// may withdraw, in an ordinary signed transaction, up to the counter, and no more than the
	// pool's own free liquidity, to a recipient it names. Before the height the byte is an unknown
	// transaction type, exactly as on every earlier release. Developer fees are paid to their
	// recipient when the loan is drawn and are not withdrawable (see LendingAccountingFixesActivationHeight).
	// 0 (the default) keeps the behaviour of every earlier release at every height. A chain rule:
	// it decides which transactions are valid, so set the same non-zero height on every validator,
	// above the chain tip, with all of them restarted together; before granting the role add its
	// name to [governance] AllowedRoles on every validator. As a top-level key it goes before the
	// first [table] of the file. See core.StateProcessor.SetLendingFeeWithdrawActivationHeight.
	LendingFeeWithdrawActivationHeight uint64 `toml:"LendingFeeWithdrawActivationHeight"`
	// PaymasterSponsorBindingActivationHeight is the first block height from which
	// a paymaster's signature on a sponsored transaction must be over a digest that
	// names the sender (PL-R5T-22). Until then the sponsor signs the same digest as
	// the sender's own envelope signature, which names no sender: one sponsor
	// signature is valid for every sender that signs the same fields, and a copy of
	// it placed in the envelope makes the sponsor the sender. From the height the
	// signature over that older digest is refused and the sponsor signs
	// types.Transaction.PaymasterSponsorDigest (the tag, the transaction digest and
	// the sender); the transaction digest, the sender's signature and transaction
	// ids do not change. 0 (the default) keeps the older rule at every height. A
	// chain rule: set the same value on every validator, above the chain tip, with
	// all of them restarted, and move every sponsor's signer (and any SDK that signs
	// for a sponsor) to SignPaymasterBound first. See
	// core.StateProcessor.SetPaymasterSponsorBinding.
	PaymasterSponsorBindingActivationHeight uint64 `toml:"PaymasterSponsorBindingActivationHeight"`
	// LoyaltyPaymasterConsentActivationHeight is the first block height from which
	// a loyalty program reward is paid only out of a wallet that is the business
	// owner's own or has a standing record that it was assigned with its consent
	// (PL-R5T-15). Until then the consent rule runs only when an assignment is made,
	// so an assignment already in state, including one that names the treasury or
	// admin wallet, keeps paying although that wallet never consented. From the
	// height such a reward is skipped (reason paymaster_not_consented); an
	// assignment made from the height on writes the record, a loyalty admin can
	// re-affirm an older assignment once the wallet has opted in again, the
	// paymaster can withdraw itself, and a registered merchant address can remove
	// itself. 0 (the default) keeps every one of these as it was. A chain rule: set
	// the same value on every validator, above the chain tip, with all of them
	// restarted together. See core.StateProcessor.SetLoyaltyPaymasterConsent.
	LoyaltyPaymasterConsentActivationHeight uint64 `toml:"LoyaltyPaymasterConsentActivationHeight"`
	// LoyaltyCounterpartyCapActivationHeight is the first block height from which a
	// base-reward cap per pair of accounts per UTC day applies when the stored
	// loyalty configuration sets none (PL-EXT-17). The code has always had the cap
	// (DailyCapCounterparty, shared by A to B and B to A), but the relaunch genesis
	// stores none, so it is off on the live chain. From the height the base reward
	// path reads the cap LoyaltyCounterpartyCapZNHBWei in its place; a cap the stored
	// configuration does set is left as it is. It bounds what one pair can earn from
	// passing the same NHB back and forth; it does NOT stop NHB passed along a
	// chain of fresh accounts, where every hop is a new pair (the owner's choice of
	// mitigation for that is open). 0 (the default), or an empty amount, leaves the
	// rule as it is. A chain rule: set the same values on every validator, above
	// the chain tip, with all of them restarted together. See
	// core.StateProcessor.SetLoyaltyCounterpartyCap.
	LoyaltyCounterpartyCapActivationHeight uint64 `toml:"LoyaltyCounterpartyCapActivationHeight"`
	// BuybackMinAskActivationHeight is the first block height from which the smallest
	// treasury buyback ask is BuybackMinAskZNHBWei instead of one whole ZNHB
	// (PL-R5T-33). An epoch holds at most 1,024 pending asks and a seller 8, an ask
	// costs only ZNHB that is refunded or filled at the settlement, and about 128
	// addresses with a few ZNHB each can fill every slot and refuse honest sellers for
	// the epoch. A larger minimum raises the ZNHB that has to be locked to do it by
	// the same factor; it does not remove the limit (the other option, letting a
	// larger ask evict the smallest, is not built). A value at or below one ZNHB, an
	// empty amount or 0 (the default) leaves the minimum as it is. A chain rule: set
	// the same values on every validator, above the chain tip, with all of them
	// restarted together. See core.StateProcessor.SetBuybackMinAsk.
	BuybackMinAskActivationHeight uint64 `toml:"BuybackMinAskActivationHeight"`
	// SubscriptionsPauseSettlementActivationHeight is the first block height from
	// which the per-block subscription settlement does nothing while the
	// subscriptions module is paused (PL-R5T-81). A pause refuses every
	// subscriptions transaction, a payer's cancel included, but the settlement that
	// runs every block never looked at the pause, so payers kept being charged each
	// cycle while unable to cancel. From the height a paused module charges nobody,
	// records no failed attempt and moves no watermark: the days skipped are found
	// again on unpause and each due subscription is charged once then. 0 (the
	// default) keeps the settlement running through a pause. A chain rule: set the
	// same value on every validator, above the chain tip, with all of them
	// restarted together. See core.StateProcessor.SetSubscriptionsPauseSettlement.
	SubscriptionsPauseSettlementActivationHeight uint64 `toml:"SubscriptionsPauseSettlementActivationHeight"`
	// SubscriptionsChargeHistoryMigrationActivationHeight is the first block height
	// from which a subscription's charge history that was written under the old key
	// (one list per subscription, before each attempt was stored under its own key and
	// a count) is read through and migrated (PL-R5T-82). The newer code never reads
	// the old key: a subscription holding history there would list as empty, restart
	// its attempt numbering at 1 and leave the old list orphaned. From the height, a
	// subscription with no count key reads its history from the old list, and the
	// first attempt recorded afterwards copies that list into one key per attempt, sets
	// the count and continues the numbering. The commit that moved the layout says no
	// subscription was ever charged on the live chain; on a chain that never wrote the
	// old key this changes nothing, and a read through to an empty key writes nothing.
	// 0 (the default) keeps the newer layout alone. A chain rule: set the same value on
	// every validator, above the chain tip, with all of them restarted together. See
	// core.StateProcessor.SetSubscriptionsChargeHistoryMigration.
	SubscriptionsChargeHistoryMigrationActivationHeight uint64 `toml:"SubscriptionsChargeHistoryMigrationActivationHeight"`
	// BuybackUnpriceableEpochRefundActivationHeight is the first block height from which a buyback epoch whose highest acceptable price cannot be computed is settled like an epoch with no reference price (nothing is bought, every seller is refunded in full) instead of failing the block that closes the epoch (PL-EXT-28). The governed discount and safety margin may be set up to 10,000 basis points, and at exactly 10,000 the highest price is zero: until the height the settlement returns "price must be positive" and every validator rejects the block that closes the epoch, so the chain stops there until the value is changed.
	// 0 (the default) leaves it off at every height. A chain rule: set the same value on
	// every validator, above the chain tip, with all of them restarted together. See
	// core.StateProcessor.SetBuybackUnpriceableEpochRefund.
	BuybackUnpriceableEpochRefundActivationHeight uint64 `toml:"BuybackUnpriceableEpochRefundActivationHeight"`
	// SwapVoucherSignedBudgetActivationHeight is the first block height from which the USD budget a swap voucher mint is held to is read only from what the mint authority signed (PL-EXT-35). Until then it is the submission's own usdAmount, which no signed digest covers, so whoever submits the voucher can write the budget that makes the treasury's curve-price guard pass. From the height the budget is the signed fiat amount of a voucher in USD, a submission whose usdAmount is set and differs from it is refused, and a voucher in any other currency is refused because nothing signed gives its value in USD. Set it only once the voucher issuer sells in USD.
	// 0 (the default) leaves it off at every height. A chain rule: set the same value on
	// every validator, above the chain tip, with all of them restarted together. See
	// core.StateProcessor.SetSwapVoucherSignedBudget.
	SwapVoucherSignedBudgetActivationHeight uint64 `toml:"SwapVoucherSignedBudgetActivationHeight"`
	// SponsoredTransferFeeCreditActivationHeight is the first block height from which the fee a paymaster pays for a sponsored NHB transfer is always credited to the transfer fee collector (PL-EXT-38). Until then the credit is made only while the transfer gas policy is enabled, and the policy is disabled whenever its free-tier spend limit is zero, whatever the fee rate: the sponsor is debited and the fee is destroyed, the shape PL-DC-08 closed in the sender's own branch. From the height the sponsored branch credits any positive fee, as the unsponsored NHB path does.
	// 0 (the default) leaves it off at every height. A chain rule: set the same value on
	// every validator, above the chain tip, with all of them restarted together. See
	// core.StateProcessor.SetSponsoredTransferFeeCredit.
	SponsoredTransferFeeCreditActivationHeight uint64 `toml:"SponsoredTransferFeeCreditActivationHeight"`
	// UniformTransferFeeActivationHeight is the first block height from which a transfer that
	// names a fee domain (the pos, p2p and otc domains of [global.Fees]) pays exactly what an
	// ordinary transfer pays: the same free allowance per wallet and asset, the same NHB and
	// ZNHB rates, and nothing on top. Until then such a transfer pays the ordinary fee AND the
	// domain's merchant rate (MDRBasisPoints, 150 NHB and 200 ZNHB in the live files), which the
	// first N transfers a month waive (FreeTierTxPerMonth). From the height the fee is still
	// credited to the domain's route wallet when one is configured for the asset (the fee
	// collector otherwise), and the domain's fee totals and fee event are still recorded; the
	// per-domain monthly counters stop advancing. A fee payer of "recipient" is not honoured: the
	// sender pays, as for any transfer. 0 (the default) leaves the domain fee as it is at every
	// height. A chain rule: set the same value on every validator, above the chain tip, with all
	// of them restarted together. See core.StateProcessor.SetUniformTransferFee.
	UniformTransferFeeActivationHeight uint64 `toml:"UniformTransferFeeActivationHeight"`
	// TransferFeeGovernanceActivationHeight is the first block height from which the transfer fee
	// schedule (the NHB and ZNHB rates and the free allowance per wallet and asset) is governed
	// by proposals of the kind policy.feeParams instead of by this file. Until then no such
	// proposal is accepted and the values of [global.Fees] (TransferFeeBps, TransferFeeBpsZNHB,
	// TransferFreeTierSpendWei) apply. From the height a passed proposal, after its timelock, is
	// stored in chain state as a pending schedule that takes effect TransferFeeGovernanceDelayBlocks
	// blocks after the block that executed it, the same block on every validator; once in force it
	// replaces the values of the file. Rates are limited to 100 basis points and an allowance of
	// zero needs an explicit flag. 0 (the default) leaves the file in charge at every height. A
	// chain rule: set the same values on every validator, above the chain tip, with all of them
	// restarted together. See core.StateProcessor.SetTransferFeeGovernance.
	TransferFeeGovernanceActivationHeight uint64 `toml:"TransferFeeGovernanceActivationHeight"`
	// TransferFeeGovernanceDelayBlocks is the delay, in blocks, between the block that executes a
	// fee proposal and the first block that charges its schedule. It is raised to 100 when lower
	// (governance.MinFeeScheduleDelayBlocks); a value below that is reported at start-up (see
	// FeeScheduleProblems). It matters only once TransferFeeGovernanceActivationHeight is set.
	TransferFeeGovernanceDelayBlocks uint64 `toml:"TransferFeeGovernanceDelayBlocks"`
	// TransferFeeStrictAllowanceActivationHeight is the first block height from which the free allowance of the transfer
	// fee (TransferFreeTierSpendWei, or the governed allowance) is strict. Until then a transfer is free in full while the
	// sender's spend so far in the asset is below the allowance, so the transfer that crosses the line is free in full (a
	// first transfer of 1,000,000 NHB pays no fee). From the height the free part of a transfer is what is left of the
	// allowance (never below zero, never more than the amount) and the fee is the asset's rate on the rest: a first
	// transfer of 1,100 NHB against an allowance of 1,000 pays 0.20% of 100. It applies to the ordinary fee of both assets,
	// so to a transfer that names a fee domain under UniformTransferFeeActivationHeight, and to a governed allowance. It
	// does not change what is recorded as spent, the window, the monthly free tier (which still waives the whole remaining
	// fee where it applies) or a sponsored transfer (the sponsor pays the fee on the whole amount, as before). 0 (the
	// default) leaves the whole-transfer rule at every height. A chain rule: set the same value on every validator, above
	// the chain tip, with all of them restarted together. See core.StateProcessor.SetTransferFeeStrictAllowance.
	TransferFeeStrictAllowanceActivationHeight uint64 `toml:"TransferFeeStrictAllowanceActivationHeight"`
	// RPCSwapOperatorNoHMAC makes nhb_swapMint, nhb_swapBurn and
	// swap_voucher_get/list/export need the operator role when RPCSwapAuth has no
	// secrets (PL-R5T-84). Those methods are exempt from the operator gate because the
	// per-partner HMAC check is the stronger control, but it runs only when secrets
	// exist; without them any valid bearer token reaches them. false (the default)
	// keeps the exemption. Not a chain rule: a node setting. Callers that use these
	// methods with a non-operator token must be moved to operator tokens or to partner
	// secrets first.
	RPCSwapOperatorNoHMAC bool `toml:"RPCSwapOperatorNoHMAC"`
	// BuybackMinAskZNHBWei is the smallest buyback ask, in ZNHB wei, as a decimal
	// string (the same notations as the other amounts, for example "100e18").
	BuybackMinAskZNHBWei string `toml:"BuybackMinAskZNHBWei"`
	// LoyaltyCounterpartyCapZNHBWei is the per-pair daily cap, in ZNHB wei, as a
	// decimal string (the same notations as the other amounts, for example "50e18").
	LoyaltyCounterpartyCapZNHBWei string `toml:"LoyaltyCounterpartyCapZNHBWei"`
	// BuybackPriceGuardActivationHeight is the first block height from which
	// governance refuses a buyback discount or safety margin of 10000 basis
	// points (PL-EXT-28). The highest price the buyback may pay is the lower of
	// the curve price after the discount and the signed reference price after the
	// margin, so either value at 10000 makes it exactly zero, and the epoch
	// settlement then refuses the price. The refusal is made when a proposal of
	// the buyback parameters kind is submitted and again when one is executed,
	// and by the one function every governance write of those two parameters goes
	// through; a proposal stored before the height is not looked at again, but
	// executing it from the height refuses the write and leaves the parameters as
	// they were. 0 (the default) leaves governance as it was. A chain rule: set
	// the same value on every validator, above the chain tip, with all of them
	// restarted together; set EpochSettlementFailsafeActivationHeight to the same
	// height or lower. See core.StateProcessor.SetBuybackPriceGuard.
	BuybackPriceGuardActivationHeight uint64 `toml:"BuybackPriceGuardActivationHeight"`
	// EpochSettlementFailsafeActivationHeight is the first block height from which
	// two causes of an error in the epoch-closing block degrade instead
	// (PL-EXT-28, PL-EXT-29 item E-1b). (1) A buyback price that is zero or cannot
	// be computed means no purchase this epoch: every ask is refunded in full, an
	// event and a log line say so, and the escrow and accrual balances are
	// unchanged but for the refunds. (2) When the admin wallet's liquid ZNHB is
	// below the reward payout, the payout is scaled down to the liquid balance, the
	// way it already is to the reward pool, and the epoch is recorded as short. 0
	// (the default) leaves both as they were: an error that fails the block. A
	// chain rule: set the same value on every validator, above the chain tip, with
	// all of them restarted together. See core.StateProcessor.SetEpochSettlementFailsafe.
	EpochSettlementFailsafeActivationHeight uint64 `toml:"EpochSettlementFailsafeActivationHeight"`
	// SaleReferenceActivationHeight is the first block height from which the price of
	// every sale from the treasury Sale Pool (the direct purchase, the swap voucher
	// mint's curve guard and the quote) is the higher of the Genesis curve's cost and
	// the latest signed outside reference price times the amount (PL-BC-D). The
	// reference is the one the treasury buyback takes (TxTypeBuybackRefPrice, signed
	// by the genesis-declared reference-price signers) and is never read from the
	// native pool or any peer-to-peer price. While the rule is on, a missing reference
	// or one older than SaleReferenceMaxAgeSeconds PAUSES sales; it never falls back
	// to the curve. 0 (the default) leaves every sale priced by the curve alone at
	// every height. Must be in force BEFORE BridgeActivationHeight is set. A chain
	// rule: set the same value, and the same two sizes below, on every validator,
	// above the chain tip, with all of them restarted together. See
	// core.ValidatorRules and core/sale_reference.go.
	SaleReferenceActivationHeight uint64 `toml:"SaleReferenceActivationHeight"`
	// SaleReferenceMaxAgeSeconds is how old, in seconds, a reference price may be and
	// still price a sale; it must be non-zero once the rule is on (zero pauses sales).
	// A reference older than this at the block that would record it is refused. The
	// size is an owner decision. A chain rule: the same value on every validator.
	SaleReferenceMaxAgeSeconds uint64 `toml:"SaleReferenceMaxAgeSeconds"`
	// SaleReferenceMaxStepBps refuses a new reference that moves more than this many
	// basis points from the previous one while that one is still fresh (0 = no
	// bound); a stale previous reference lets any new one in, so a stuck feed can
	// always be replaced. The size is an owner decision. A chain rule: the same value
	// on every validator.
	SaleReferenceMaxStepBps uint64 `toml:"SaleReferenceMaxStepBps"`
}

// RPCRouteRateLimit captures optional per-method quota overrides for the JSON-RPC server.
type RPCRouteRateLimit struct {
	MaxTxPerWindow        int `toml:"MaxTxPerWindow"`
	MaxTxPerIP            int `toml:"MaxTxPerIP"`
	MaxTxPerIdentity      int `toml:"MaxTxPerIdentity"`
	MaxTxPerChain         int `toml:"MaxTxPerChain"`
	MaxTxPerIdentityChain int `toml:"MaxTxPerIdentityChain"`
}

// NetworkSecurity captures TLS and shared-secret settings for the internal gRPC
// bridge between consensusd and p2pd.
type NetworkSecurity struct {
	ServerTLSCertFile         string   `toml:"ServerTLSCertFile"`
	ServerTLSKeyFile          string   `toml:"ServerTLSKeyFile"`
	ServerCAFile              string   `toml:"ServerCAFile"`
	ClientCAFile              string   `toml:"ClientCAFile"`
	ClientTLSCertFile         string   `toml:"ClientTLSCertFile"`
	ClientTLSKeyFile          string   `toml:"ClientTLSKeyFile"`
	AllowInsecure             bool     `toml:"AllowInsecure"`
	AllowUnauthenticatedReads bool     `toml:"AllowUnauthenticatedReads"`
	SharedSecret              string   `toml:"SharedSecret"`
	SharedSecretFile          string   `toml:"SharedSecretFile"`
	SharedSecretEnv           string   `toml:"SharedSecretEnv"`
	AuthorizationHeader       string   `toml:"AuthorizationHeader"`
	AllowedClientCommonNames  []string `toml:"AllowedClientCommonNames"`
	ServerName                string   `toml:"ServerName"`
	StreamQueueSize           int      `toml:"StreamQueueSize"`
	RelayDropLogRatio         float64  `toml:"RelayDropLogRatio"`
}

// AuthorizationHeaderName returns the metadata header that carries the
// shared-secret token. Defaults to "authorization" when unspecified.
func (ns NetworkSecurity) AuthorizationHeaderName() string {
	header := strings.TrimSpace(ns.AuthorizationHeader)
	if header == "" {
		return "authorization"
	}
	return strings.ToLower(header)
}

// ResolveSharedSecret locates the shared-secret token following the precedence
// order of environment variable, external file, and inline configuration.
// Relative file paths are resolved against baseDir when provided.
func (ns NetworkSecurity) ResolveSharedSecret(baseDir string, lookup func(string) (string, bool)) (string, error) {
	if key := strings.TrimSpace(ns.SharedSecretEnv); key != "" && lookup != nil {
		if value, ok := lookup(key); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed, nil
			}
		}
	}

	if path := strings.TrimSpace(ns.SharedSecretFile); path != "" {
		if baseDir != "" && !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if secret := strings.TrimSpace(string(data)); secret != "" {
			return secret, nil
		}
	}

	return strings.TrimSpace(ns.SharedSecret), nil
}

func defaultGlobalConfig() Global {
	return Global{
		Governance: Governance{
			QuorumBPS:        6000,
			PassThresholdBPS: 5000,
			VotingPeriodSecs: 604800,
		},
		Slashing: Slashing{
			MinWindowSecs: 60,
			MaxWindowSecs: 600,
		},
		Mempool: Mempool{MaxBytes: 16 << 20, POSReservationBPS: consensus.DefaultPOSReservationBPS},
		Blocks:  Blocks{MaxTxs: 500},
		Staking: Staking{
			AprBps:                1250,
			PayoutPeriodDays:      30,
			UnbondingDays:         7,
			MinStakeWei:           "10000000000000000000000",
			MaxEmissionPerYearWei: "5000000000000000000",
			RewardAsset:           "ZNHB",
			CompoundDefault:       false,
		},
		Pauses: Pauses{},
		Paymaster: Paymaster{
			MerchantDailyCapWei: "0",
			GlobalDailyCapWei:   "0",
			AutoTopUp: PaymasterAutoTopUp{
				Token:          "NHB",
				MinBalanceWei:  "0",
				TopUpAmountWei: "0",
				DailyCapWei:    "0",
				Governance: PaymasterAutoTopUpGovernance{
					MinterRole:   "ROLE_PAYMASTER_AUTOFUND",
					ApproverRole: "ROLE_PAYMASTER_ADMIN",
				},
			},
		},
		Fees: Fees{
			FreeTierTxPerMonth: DefaultFreeTierTxPerMonth,
			MDRBasisPoints:     DefaultMDRBasisPoints,
			OwnerWallet:        "nhb1tctz3yvhrwztnp6ds3s48qp4jgfujcvhgxxpka",
			// 1000 NHB at 18 decimals -- see docs/issue30.md item 7. The
			// previous "1000" (0.000000000000001 NHB) exhausted the free
			// tier on a wallet's first transaction.
			TransferFreeTierSpendWei: "1000000000000000000000",
			TransferFreeTierWindow:   "lifetime",
			// 20 bps (0.20%) once the free tier is exhausted -- comfortably
			// undercuts a single exchange trading leg (typically 10-50bps),
			// let alone the two-legged swap-in/swap-out round trip NHB is
			// meant to replace, while still scaling with real volume instead
			// of being negligible per-transaction. See docs/issue30.md item 7b.
			TransferFeeBps: 20,
			// 10 bps (0.10%) once the free tier is exhausted -- ZNHB's own,
			// separately configurable rate, deliberately lower than NHB's
			// since ZNHB is a lower-priced asset with its own use case.
			TransferFeeBpsZNHB: 10,
			Assets: []FeeAsset{
				{Asset: fees.AssetNHB, MDRBasisPoints: DefaultMDRBasisPoints, OwnerWallet: "nhb1tctz3yvhrwztnp6ds3s48qp4jgfujcvhgxxpka"},
				{Asset: fees.AssetZNHB, MDRBasisPoints: DefaultMDRBasisPoints, OwnerWallet: "znhb19l75s7jkyzxp4z7lj3ddgn9r89y3kps54wpv0w"},
			},
		},
		Loyalty: Loyalty{
			Dynamic: LoyaltyDynamic{
				TargetBPS:                   defaultLoyaltyTargetBPS,
				MinBPS:                      defaultLoyaltyMinBPS,
				MaxBPS:                      defaultLoyaltyMaxBPS,
				SmoothingStepBPS:            defaultLoyaltySmoothingStepBPS,
				CoverageMax:                 defaultLoyaltyCoverageMax,
				CoverageLookbackDays:        defaultLoyaltyCoverageLookbackDays,
				DailyCapPctOf7dFees:         defaultLoyaltyDailyCapPctOf7dFees,
				DailyCapUSD:                 defaultLoyaltyDailyCapUSD,
				YearlyCapPctOfInitialSupply: defaultLoyaltyYearlyCapPctOfInitialSupply,
				PriceGuard: LoyaltyPriceGuard{
					Enabled:                    true,
					PricePair:                  defaultLoyaltyPricePair,
					TwapWindowSeconds:          defaultLoyaltyPriceGuardTwapWindowSeconds,
					MaxDeviationBPS:            defaultLoyaltyPriceGuardMaxDeviation,
					PriceMaxAgeSeconds:         defaultLoyaltyPriceGuardMaxAgeSeconds,
					FallbackMinEmissionZNHBWei: defaultLoyaltyPriceGuardFallbackMinEmission,
					UseLastGoodPriceFallback:   true,
				},
				EnableProRate:  true,
				EnforceProRate: defaultLoyaltyEnforceProRate,
			},
		},
	}
}

// defaultMinBlockInterval is one block a second: about the average an idle pair of
// validators has kept, over hours, while it lost rounds to itself (0.6 to 1 blocks a
// second). A config that leaves the key out keeps it; "0s"
// turns the wait off, and the blocks then come as fast as the validators can make them.
const defaultMinBlockInterval = time.Second

func defaultConsensusConfig() Consensus {
	return Consensus{
		ProposalTimeout:  2 * time.Second,
		PrevoteTimeout:   2 * time.Second,
		PrecommitTimeout: 2 * time.Second,
		CommitTimeout:    4 * time.Second,
		MinBlockInterval: defaultMinBlockInterval,
	}
}

const (
	defaultBlockTimestampToleranceSeconds = 5
	defaultStreamQueueSize                = 128
	defaultRelayDropLogRatio              = 0.1
	// DefaultMempoolMaxTransactions bounds pending transactions when no explicit limit is provided.
	DefaultMempoolMaxTransactions               = 4000
	defaultLoyaltyTargetBPS                     = 50
	defaultLoyaltyMinBPS                        = 25
	defaultLoyaltyMaxBPS                        = 100
	defaultLoyaltySmoothingStepBPS              = 5
	defaultLoyaltyCoverageMax                   = 0.50
	defaultLoyaltyCoverageLookbackDays          = 7
	defaultLoyaltyDailyCapPctOf7dFees           = 0.60
	defaultLoyaltyDailyCapUSD                   = 5_000.0
	defaultLoyaltyYearlyCapPctOfInitialSupply   = 10.0
	defaultLoyaltyPricePair                     = "ZNHB/USD"
	defaultLoyaltyPriceGuardTwapWindowSeconds   = uint32(7_200)
	defaultLoyaltyPriceGuardMaxDeviation        = 300
	defaultLoyaltyPriceGuardMaxAgeSeconds       = uint32(600)
	defaultLoyaltyPriceGuardFallbackMinEmission = "0"
	defaultLoyaltyEnforceProRate                = true
)

// P2PSection captures nested configuration for the peer-to-peer subsystem.
type P2PSection struct {
	// NetworkID is populated after decoding from NetworkIDRaw (see Load), not
	// directly by TOML: the TOML integer type is 64-bit signed, but NHBCoin
	// network IDs can exceed int64's range, so the raw value is decoded as a
	// string and parsed with strconv.ParseUint instead.
	NetworkID    uint64 `toml:"-"`
	NetworkIDRaw string `toml:"NetworkId"`
	// ExternalAddress is this node's own publicly-dialable host:port (e.g.
	// its EC2 public IP on the P2P port). ListenAddress is typically bound
	// to 0.0.0.0 so the process can actually accept connections, but an
	// unspecified address like that is not something a remote peer can
	// dial back to, so it's filtered out of the handshake's self-reported
	// ListenAddrs. Without an explicit external address, a peer that first
	// reaches this node via an inbound connection has no way to record a
	// usable address for reconnecting later -- it falls back to that one
	// connection's ephemeral source port, which is never a listening
	// socket, so every subsequent reconnect attempt permanently fails.
	ExternalAddress     string   `toml:"ExternalAddress"`
	MaxPeers            int      `toml:"MaxPeers"`
	MaxInbound          int      `toml:"MaxInbound"`
	MaxOutbound         int      `toml:"MaxOutbound"`
	MinPeers            int      `toml:"MinPeers"`
	OutboundPeers       int      `toml:"OutboundPeers"`
	Bootnodes           []string `toml:"Bootnodes"`
	PersistentPeers     []string `toml:"PersistentPeers"`
	Seeds               []string `toml:"Seeds"`
	BanScore            int      `toml:"BanScore"`
	GreyScore           int      `toml:"GreyScore"`
	RateMsgsPerSec      float64  `toml:"RateMsgsPerSec"`
	Burst               float64  `toml:"Burst"`
	HandshakeTimeoutMs  int      `toml:"HandshakeTimeoutMs"`
	PingIntervalSeconds int      `toml:"PingIntervalSeconds"`
	PingTimeoutSeconds  int      `toml:"PingTimeoutSeconds"`
	BanDurationSeconds  int      `toml:"BanDurationSeconds"`
	DialBackoffSeconds  int      `toml:"DialBackoffSeconds"`
	PEX                 *bool    `toml:"PEX"`
	// HandshakeChallenge is how this node treats the challenge-response extension of the p2p
	// handshake (PL-R5T-20, PL-DC-25): "auto" (the default) offers it and proves its key to a peer
	// that offers it too, treating any other peer as a v1 peer; "off" behaves exactly like a v1
	// node; "require" refuses a peer that does not offer it, for use once every peer has upgraded.
	// Not a chain rule: it changes who a node talks to, never what a block holds.
	HandshakeChallenge string `toml:"HandshakeChallenge"`
}

// PotsoConfig groups POTSO-specific configuration segments.
type PotsoConfig struct {
	Rewards PotsoRewardsConfig `toml:"rewards"`
	Weights PotsoWeightsConfig `toml:"weights"`
	Abuse   PotsoAbuseConfig   `toml:"abuse"`
}

// PotsoRewardsConfig mirrors the TOML structure for POTSO reward distribution parameters.
type PotsoRewardsConfig struct {
	EpochLengthBlocks  uint64 `toml:"EpochLengthBlocks"`
	AlphaStakeBps      uint64 `toml:"AlphaStakeBps"`
	MinPayoutWei       string `toml:"MinPayoutWei"`
	EmissionPerEpoch   string `toml:"EmissionPerEpoch"`
	TreasuryAddress    string `toml:"TreasuryAddress"`
	MaxWinnersPerEpoch uint64 `toml:"MaxWinnersPerEpoch"`
	CarryRemainder     bool   `toml:"CarryRemainder"`
	PayoutMode         string `toml:"PayoutMode"`
}

// PotsoWeightsConfig mirrors the `[potso.weights]` TOML section.
type PotsoWeightsConfig struct {
	AlphaStakeBps         uint64 `toml:"AlphaStakeBps"`
	TxWeightBps           uint64 `toml:"TxWeightBps"`
	EscrowWeightBps       uint64 `toml:"EscrowWeightBps"`
	UptimeWeightBps       uint64 `toml:"UptimeWeightBps"`
	MaxEngagementPerEpoch uint64 `toml:"MaxEngagementPerEpoch"`
	MinStakeToWinWei      string `toml:"MinStakeToWinWei"`
	MinEngagementToWin    uint64 `toml:"MinEngagementToWin"`
	DecayHalfLifeEpochs   uint64 `toml:"DecayHalfLifeEpochs"`
	TopKWinners           uint64 `toml:"TopKWinners"`
	TieBreak              string `toml:"TieBreak"`
}

// PotsoAbuseConfig captures anti-abuse controls for POTSO emissions and weights.
type PotsoAbuseConfig struct {
	MinStakeToEarnWei      string `toml:"MinStakeToEarnWei"`
	QuadraticTxDampenAfter uint64 `toml:"QuadraticTxDampenAfter"`
	QuadraticTxDampenPower uint64 `toml:"QuadraticTxDampenPower"`
	MaxUserShareBps        uint64 `toml:"MaxUserShareBps"`
}

// MempoolConfig allows operators to tune the size of the transaction pool.
type MempoolConfig struct {
	MaxTransactions int  `toml:"MaxTransactions"`
	AllowUnlimited  bool `toml:"AllowUnlimited"`
}

// GovConfig captures the governance policy knobs controlling proposal flow
// without embedding business logic in the state machine.
type GovConfig struct {
	MinDepositWei                  string   `toml:"MinDepositWei"`
	VotingPeriodSeconds            uint64   `toml:"VotingPeriodSeconds"`
	TimelockSeconds                uint64   `toml:"TimelockSeconds"`
	QuorumBps                      uint64   `toml:"QuorumBps"`
	PassThresholdBps               uint64   `toml:"PassThresholdBps"`
	AllowedParams                  []string `toml:"AllowedParams"`
	AllowedRoles                   []string `toml:"AllowedRoles"`
	TreasuryAllowList              []string `toml:"TreasuryAllowList"`
	BlockTimestampToleranceSeconds uint64   `toml:"BlockTimestampToleranceSeconds"`

	// RejectAmbiguousParamKeysFromUnix is the block timestamp (Unix seconds)
	// from which a parameter payload that repeats one parameter under keys
	// differing only in surrounding whitespace, with different values, is
	// rejected. Zero (the default) leaves the earlier behaviour in place.
	RejectAmbiguousParamKeysFromUnix uint64 `toml:"RejectAmbiguousParamKeysFromUnix"`

	// EnforceWindowFloors makes the node refuse to start when VotingPeriodSeconds
	// or TimelockSeconds is below its floor (MinVotingPeriodSeconds,
	// MinTimelockSeconds; see GovernancePolicyProblems). Off by default: the node
	// then only warns. It decides nothing about any block, only whether this node
	// starts, so it needs no coordination between validators.
	EnforceWindowFloors bool `toml:"EnforceWindowFloors"`
}

type LoadOption func(*loadOptions)

type loadOptions struct {
	keystorePassphrase       string
	keystorePassphraseSource func() (string, error)
}

// WithKeystorePassphrase provides an explicit passphrase to use when the loader
// needs to create or update the validator keystore.
func WithKeystorePassphrase(passphrase string) LoadOption {
	return func(opts *loadOptions) {
		opts.keystorePassphrase = passphrase
	}
}

// WithKeystorePassphraseSource registers a lazy resolver that is invoked only
// when a passphrase is required (for example, on first-run when no keystore
// exists). The resolved value is cached so subsequent calls return the same
// passphrase.
func WithKeystorePassphraseSource(source func() (string, error)) LoadOption {
	return func(opts *loadOptions) {
		opts.keystorePassphraseSource = source
	}
}

// Load loads the configuration from the given path.
// networkIDPattern matches a bare (unquoted) integer literal assigned to
// NetworkId so Load can quote it before handing the document to the TOML
// decoder. TOML's integer type is 64-bit signed and cannot represent every
// valid uint64 network ID; already-quoted values are left untouched.
var networkIDPattern = regexp.MustCompile(`(?m)^([ \t]*NetworkId[ \t]*=[ \t]*)(\d+)([ \t]*)$`)

// quoteOversizedNetworkID rewrites a bare NetworkId integer literal as a
// quoted string in-memory, without touching the file on disk, so it can be
// decoded as a string and parsed with strconv.ParseUint regardless of
// magnitude. See P2PSection.NetworkIDRaw.
func quoteOversizedNetworkID(raw []byte) []byte {
	return networkIDPattern.ReplaceAll(raw, []byte(`$1"$2"$3`))
}

func Load(path string, opts ...LoadOption) (*Config, error) {
	options := loadOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	passphrase := options.keystorePassphrase
	havePassphrase := passphrase != ""
	resolvePassphrase := func() (string, error) {
		if havePassphrase {
			return passphrase, nil
		}
		if options.keystorePassphraseSource == nil {
			return "", nil
		}
		value, err := options.keystorePassphraseSource()
		if err != nil {
			return "", err
		}
		passphrase = value
		havePassphrase = true
		return passphrase, nil
	}

	cfg := &Config{}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		resolved, err := resolvePassphrase()
		if err != nil {
			return nil, err
		}
		return createDefault(path, resolved)
	}

	rawConfig, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	meta, err := toml.Decode(string(quoteOversizedNetworkID(rawConfig)), cfg)
	if err != nil {
		return nil, err
	}

	for _, undecoded := range meta.Undecoded() {
		if len(undecoded) == 1 && undecoded[0] == "ValidatorKey" {
			return nil, fmt.Errorf("config file %s uses deprecated ValidatorKey field; run nhbctl migrate-keystore", path)
		}
		cfg.UndecodedKeys = append(cfg.UndecodedKeys, strings.Join(undecoded, "."))
	}
	sort.Strings(cfg.UndecodedKeys)

	if raw := strings.TrimSpace(cfg.P2P.NetworkIDRaw); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("config file %s: parse p2p.NetworkId: %w", path, err)
		}
		cfg.P2P.NetworkID = parsed
	}

	if cfg.ValidatorKMSURI == "" && cfg.ValidatorKMSEnv == "" {
		if err := ensureKeystore(path, cfg, resolvePassphrase); err != nil {
			return nil, err
		}
	}

	cfg.mergeP2PFromTopLevel()
	cfg.Lending.EnsureDefaults()
	cfg.Subscriptions.EnsureDefaults()
	cfg.ensureMempoolDefaults()
	cfg.ensureGlobalDefaults(meta)
	cfg.ensureConsensusDefaults(meta)

	if strings.TrimSpace(cfg.NetworkName) == "" {
		cfg.NetworkName = "nhb-local"
	}
	if cfg.Bootnodes == nil {
		cfg.Bootnodes = []string{}
	}
	if cfg.PersistentPeers == nil {
		cfg.PersistentPeers = []string{}
	}
	if cfg.P2P.Seeds == nil {
		cfg.P2P.Seeds = []string{}
	}
	if len(cfg.Bootnodes) == 0 && len(cfg.BootstrapPeers) > 0 {
		cfg.Bootnodes = append([]string{}, cfg.BootstrapPeers...)
	}
	cfg.BootstrapPeers = nil

	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = 64
	}
	if cfg.MaxInbound <= 0 || cfg.MaxInbound > cfg.MaxPeers {
		cfg.MaxInbound = cfg.MaxPeers
	}
	if cfg.MaxOutbound <= 0 || cfg.MaxOutbound > cfg.MaxPeers {
		cfg.MaxOutbound = cfg.MaxPeers
	}
	if cfg.MinPeers <= 0 || cfg.MinPeers > cfg.MaxPeers {
		cfg.MinPeers = cfg.MaxPeers / 2
		if cfg.MinPeers <= 0 {
			cfg.MinPeers = 1
		}
	}
	if cfg.OutboundPeers <= 0 || cfg.OutboundPeers > cfg.MaxOutbound {
		cfg.OutboundPeers = cfg.MaxOutbound
	}
	if cfg.PeerBanSeconds <= 0 {
		cfg.PeerBanSeconds = int((60 * time.Minute).Seconds())
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = int((90 * time.Second).Seconds())
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = int((5 * time.Second).Seconds())
	}
	if cfg.MaxMsgBytes <= 0 {
		cfg.MaxMsgBytes = 1 << 20
	}
	if cfg.MaxMsgsPerSecond <= 0 {
		cfg.MaxMsgsPerSecond = 32
	}
	if strings.TrimSpace(cfg.ClientVersion) == "" {
		cfg.ClientVersion = "nhbchain/node"
	}
	if cfg.NetworkSecurity.StreamQueueSize <= 0 {
		cfg.NetworkSecurity.StreamQueueSize = defaultStreamQueueSize
	}
	if cfg.NetworkSecurity.RelayDropLogRatio <= 0 {
		cfg.NetworkSecurity.RelayDropLogRatio = defaultRelayDropLogRatio
	} else if cfg.NetworkSecurity.RelayDropLogRatio > 1 {
		cfg.NetworkSecurity.RelayDropLogRatio = 1
	}
	if cfg.RPCMaxTxPerWindow <= 0 {
		cfg.RPCMaxTxPerWindow = 5
	}
	if cfg.RPCMaxTxPerIP <= 0 {
		cfg.RPCMaxTxPerIP = cfg.RPCMaxTxPerWindow
	}
	if cfg.RPCMaxTxPerIdentity <= 0 {
		cfg.RPCMaxTxPerIdentity = cfg.RPCMaxTxPerWindow
	}
	if cfg.RPCMaxTxPerChain <= 0 {
		cfg.RPCMaxTxPerChain = cfg.RPCMaxTxPerWindow
	}
	if cfg.RPCMaxTxPerIdentityChain <= 0 {
		minLimit := cfg.RPCMaxTxPerIdentity
		if minLimit <= 0 || (cfg.RPCMaxTxPerChain > 0 && cfg.RPCMaxTxPerChain < minLimit) {
			minLimit = cfg.RPCMaxTxPerChain
		}
		if minLimit <= 0 {
			minLimit = cfg.RPCMaxTxPerWindow
		}
		cfg.RPCMaxTxPerIdentityChain = minLimit
	}
	if cfg.RPCRateLimitWindow <= 0 {
		cfg.RPCRateLimitWindow = int((time.Minute).Seconds())
	}
	if cfg.RPCCallerMetadataMaxTTL <= 0 {
		cfg.RPCCallerMetadataMaxTTL = int((15 * time.Minute).Seconds())
	}

	if isProductionNetworkName(cfg.NetworkName) {
		cfg.Governance.BlockTimestampToleranceSeconds = 5
	} else if cfg.Governance.BlockTimestampToleranceSeconds == 0 {
		cfg.Governance.BlockTimestampToleranceSeconds = defaultBlockTimestampToleranceSeconds
	}

	if strings.TrimSpace(cfg.Potso.Rewards.MinPayoutWei) == "" {
		cfg.Potso.Rewards.MinPayoutWei = "0"
	}
	if strings.TrimSpace(cfg.Potso.Rewards.EmissionPerEpoch) == "" {
		cfg.Potso.Rewards.EmissionPerEpoch = "1000000000000000000"
	}

	weightDefaults := potso.DefaultWeightParams()
	if cfg.Potso.Weights.AlphaStakeBps == 0 {
		cfg.Potso.Weights.AlphaStakeBps = weightDefaults.AlphaStakeBps
	}
	if cfg.Potso.Weights.TxWeightBps == 0 {
		cfg.Potso.Weights.TxWeightBps = weightDefaults.TxWeightBps
	}
	if cfg.Potso.Weights.EscrowWeightBps == 0 {
		cfg.Potso.Weights.EscrowWeightBps = weightDefaults.EscrowWeightBps
	}
	if cfg.Potso.Weights.UptimeWeightBps == 0 {
		cfg.Potso.Weights.UptimeWeightBps = weightDefaults.UptimeWeightBps
	}
	if cfg.Potso.Weights.MaxEngagementPerEpoch == 0 {
		cfg.Potso.Weights.MaxEngagementPerEpoch = weightDefaults.MaxEngagementPerEpoch
	}
	if strings.TrimSpace(cfg.Potso.Weights.MinStakeToWinWei) == "" {
		cfg.Potso.Weights.MinStakeToWinWei = weightDefaults.MinStakeToWinWei.String()
	}
	if strings.TrimSpace(cfg.Potso.Abuse.MinStakeToEarnWei) == "" {
		cfg.Potso.Abuse.MinStakeToEarnWei = weightDefaults.MinStakeToEarnWei.String()
	}
	if cfg.Potso.Weights.DecayHalfLifeEpochs == 0 {
		cfg.Potso.Weights.DecayHalfLifeEpochs = weightDefaults.DecayHalfLifeEpochs
	}
	if cfg.Potso.Weights.TopKWinners == 0 {
		cfg.Potso.Weights.TopKWinners = weightDefaults.TopKWinners
	}
	if strings.TrimSpace(cfg.Potso.Weights.TieBreak) == "" {
		cfg.Potso.Weights.TieBreak = string(weightDefaults.TieBreak)
	}
	if cfg.Potso.Abuse.QuadraticTxDampenPower == 0 {
		cfg.Potso.Abuse.QuadraticTxDampenPower = weightDefaults.QuadraticTxDampenPower
	}

	if strings.TrimSpace(cfg.Governance.MinDepositWei) == "" {
		cfg.Governance.MinDepositWei = "1000e18"
	}
	if cfg.Governance.VotingPeriodSeconds == 0 {
		cfg.Governance.VotingPeriodSeconds = 604800
	}
	if cfg.Governance.TimelockSeconds == 0 {
		cfg.Governance.TimelockSeconds = 172800
	}
	if cfg.Governance.QuorumBps == 0 {
		cfg.Governance.QuorumBps = 2000
	}
	if cfg.Governance.PassThresholdBps == 0 {
		cfg.Governance.PassThresholdBps = 5000
	}
	if len(cfg.Governance.AllowedParams) == 0 {
		cfg.Governance.AllowedParams = append([]string(nil), defaultAllowedGovernanceParams...)
	}

	cfg.Swap = cfg.Swap.Normalise()
	cfg.syncTopLevelToP2P()

	if _, err := ParseAmount(cfg.BuybackMinAskZNHBWei); err != nil {
		return nil, fmt.Errorf("BuybackMinAskZNHBWei: %w", err)
	}
	if _, err := ParseAmount(cfg.LoyaltyCounterpartyCapZNHBWei); err != nil {
		return nil, fmt.Errorf("LoyaltyCounterpartyCapZNHBWei: %w", err)
	}
	if _, err := ParseAmount(cfg.LoyaltyBudgetFloorZNHBWei); err != nil {
		return nil, fmt.Errorf("LoyaltyBudgetFloorZNHBWei: %w", err)
	}

	// The check names the same production networks as the block timestamp
	// tolerance above. It used to compare with "mainnet" alone, a name no
	// deployment of this chain uses, so it never fired (ledger PL-EXT-74).
	if isProductionNetworkName(cfg.NetworkName) && len(cfg.RPCSwapAuth.Secrets) == 0 {
		return nil, fmt.Errorf("swap RPC authentication secrets are required for mainnet deployment")
	}
	if err := cfg.RPCSwapAuth.validatePartnerProviders(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validatePartnerProviders refuses a PartnerProviders line that cannot mean what
// the operator meant: a key the node has no secret for (a typo would leave the
// partner it was meant for reading nothing), or a list with no provider in it
// (or a blank one) that would read as "no scope". The error names the key, never
// a secret.
func (a RPCSwapAuth) validatePartnerProviders() error {
	if len(a.PartnerProviders) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(a.Secrets))
	for key, secret := range a.Secrets {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" || strings.TrimSpace(secret) == "" {
			continue
		}
		known[trimmedKey] = struct{}{}
	}
	for key, providers := range a.PartnerProviders {
		trimmedKey := strings.TrimSpace(key)
		if _, ok := known[trimmedKey]; !ok {
			return fmt.Errorf("RPCSwapAuth.PartnerProviders names the key %q, which is not in RPCSwapAuth.Secrets", key)
		}
		if len(providers) == 0 {
			return fmt.Errorf("RPCSwapAuth.PartnerProviders lists no provider for the key %q", key)
		}
		for _, provider := range providers {
			if strings.TrimSpace(provider) == "" {
				return fmt.Errorf("RPCSwapAuth.PartnerProviders has a blank provider name for the key %q", key)
			}
		}
	}
	return nil
}

func (cfg *Config) ensureMempoolDefaults() {
	if cfg.Mempool.AllowUnlimited {
		if cfg.Mempool.MaxTransactions < 0 {
			cfg.Mempool.MaxTransactions = 0
		}
		return
	}
	if cfg.Mempool.MaxTransactions <= 0 {
		cfg.Mempool.MaxTransactions = DefaultMempoolMaxTransactions
	}
}

func (cfg *Config) ensureGlobalDefaults(meta toml.MetaData) {
	defaults := defaultGlobalConfig()

	if !meta.IsDefined("global", "Governance", "QuorumBPS") {
		cfg.Global.Governance.QuorumBPS = defaults.Governance.QuorumBPS
	}
	if !meta.IsDefined("global", "Governance", "PassThresholdBPS") {
		cfg.Global.Governance.PassThresholdBPS = defaults.Governance.PassThresholdBPS
	}
	if !meta.IsDefined("global", "Governance", "VotingPeriodSecs") {
		cfg.Global.Governance.VotingPeriodSecs = defaults.Governance.VotingPeriodSecs
	}

	if !meta.IsDefined("global", "Slashing", "MinWindowSecs") {
		cfg.Global.Slashing.MinWindowSecs = defaults.Slashing.MinWindowSecs
	}
	if !meta.IsDefined("global", "Slashing", "MaxWindowSecs") {
		cfg.Global.Slashing.MaxWindowSecs = defaults.Slashing.MaxWindowSecs
	}
	if cfg.Global.Slashing.MaxWindowSecs < cfg.Global.Slashing.MinWindowSecs {
		cfg.Global.Slashing.MaxWindowSecs = cfg.Global.Slashing.MinWindowSecs
	}

	if !meta.IsDefined("global", "Mempool", "MaxBytes") {
		cfg.Global.Mempool.MaxBytes = defaults.Mempool.MaxBytes
	}
	if !meta.IsDefined("global", "Mempool", "POSReservationBPS") {
		cfg.Global.Mempool.POSReservationBPS = defaults.Mempool.POSReservationBPS
	}
	if !meta.IsDefined("global", "Blocks", "MaxTxs") {
		cfg.Global.Blocks.MaxTxs = defaults.Blocks.MaxTxs
	}

	if !meta.IsDefined("global", "Staking", "AprBps") {
		cfg.Global.Staking.AprBps = defaults.Staking.AprBps
	}
	if !meta.IsDefined("global", "Staking", "PayoutPeriodDays") {
		cfg.Global.Staking.PayoutPeriodDays = defaults.Staking.PayoutPeriodDays
	}
	if !meta.IsDefined("global", "Staking", "UnbondingDays") {
		cfg.Global.Staking.UnbondingDays = defaults.Staking.UnbondingDays
	}
	if strings.TrimSpace(cfg.Global.Staking.MinStakeWei) == "" {
		cfg.Global.Staking.MinStakeWei = defaults.Staking.MinStakeWei
	}
	if strings.TrimSpace(cfg.Global.Staking.MaxEmissionPerYearWei) == "" {
		cfg.Global.Staking.MaxEmissionPerYearWei = defaults.Staking.MaxEmissionPerYearWei
	}
	if strings.TrimSpace(cfg.Global.Staking.RewardAsset) == "" {
		cfg.Global.Staking.RewardAsset = defaults.Staking.RewardAsset
	}
	if !meta.IsDefined("global", "Staking", "CompoundDefault") {
		cfg.Global.Staking.CompoundDefault = defaults.Staking.CompoundDefault
	}

	if strings.TrimSpace(cfg.Global.Paymaster.MerchantDailyCapWei) == "" {
		cfg.Global.Paymaster.MerchantDailyCapWei = defaults.Paymaster.MerchantDailyCapWei
	}
	if strings.TrimSpace(cfg.Global.Paymaster.GlobalDailyCapWei) == "" {
		cfg.Global.Paymaster.GlobalDailyCapWei = defaults.Paymaster.GlobalDailyCapWei
	}
	if !meta.IsDefined("global", "Paymaster", "DeviceDailyTxCap") {
		cfg.Global.Paymaster.DeviceDailyTxCap = defaults.Paymaster.DeviceDailyTxCap
	}
	if !meta.IsDefined("global", "Paymaster", "AutoTopUp", "Token") {
		cfg.Global.Paymaster.AutoTopUp.Token = defaults.Paymaster.AutoTopUp.Token
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.MinBalanceWei) == "" {
		cfg.Global.Paymaster.AutoTopUp.MinBalanceWei = defaults.Paymaster.AutoTopUp.MinBalanceWei
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.TopUpAmountWei) == "" {
		cfg.Global.Paymaster.AutoTopUp.TopUpAmountWei = defaults.Paymaster.AutoTopUp.TopUpAmountWei
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.DailyCapWei) == "" {
		cfg.Global.Paymaster.AutoTopUp.DailyCapWei = defaults.Paymaster.AutoTopUp.DailyCapWei
	}
	if !meta.IsDefined("global", "Paymaster", "AutoTopUp", "CooldownSeconds") {
		cfg.Global.Paymaster.AutoTopUp.CooldownSeconds = defaults.Paymaster.AutoTopUp.CooldownSeconds
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.Governance.FundingAccount) == "" {
		cfg.Global.Paymaster.AutoTopUp.Governance.FundingAccount = strings.TrimSpace(defaults.Paymaster.AutoTopUp.Governance.FundingAccount)
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.Governance.Minter) == "" {
		cfg.Global.Paymaster.AutoTopUp.Governance.Minter = strings.TrimSpace(defaults.Paymaster.AutoTopUp.Governance.Minter)
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.Governance.Approver) == "" {
		cfg.Global.Paymaster.AutoTopUp.Governance.Approver = strings.TrimSpace(defaults.Paymaster.AutoTopUp.Governance.Approver)
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.Governance.MinterRole) == "" {
		cfg.Global.Paymaster.AutoTopUp.Governance.MinterRole = defaults.Paymaster.AutoTopUp.Governance.MinterRole
	}
	if strings.TrimSpace(cfg.Global.Paymaster.AutoTopUp.Governance.ApproverRole) == "" {
		cfg.Global.Paymaster.AutoTopUp.Governance.ApproverRole = defaults.Paymaster.AutoTopUp.Governance.ApproverRole
	}
	if !meta.IsDefined("global", "Fees", "FreeTierTxPerMonth") {
		cfg.Global.Fees.FreeTierTxPerMonth = defaults.Fees.FreeTierTxPerMonth
	}
	if !meta.IsDefined("global", "Fees", "MDRBasisPoints") {
		cfg.Global.Fees.MDRBasisPoints = defaults.Fees.MDRBasisPoints
	}
	if !meta.IsDefined("global", "Fees", "OwnerWallet") {
		cfg.Global.Fees.OwnerWallet = defaults.Fees.OwnerWallet
	}
	if !meta.IsDefined("global", "Fees", "TransferFreeTierSpendWei") {
		cfg.Global.Fees.TransferFreeTierSpendWei = defaults.Fees.TransferFreeTierSpendWei
	}
	if !meta.IsDefined("global", "Fees", "TransferFreeTierWindow") {
		cfg.Global.Fees.TransferFreeTierWindow = defaults.Fees.TransferFreeTierWindow
	}
	if !meta.IsDefined("global", "Fees", "TransferFeeCollector") {
		cfg.Global.Fees.TransferFeeCollector = defaults.Fees.TransferFeeCollector
	}
	if !meta.IsDefined("global", "Fees", "TransferFeeBps") {
		cfg.Global.Fees.TransferFeeBps = defaults.Fees.TransferFeeBps
	}
	if !meta.IsDefined("global", "Fees", "TransferFeeBpsZNHB") {
		cfg.Global.Fees.TransferFeeBpsZNHB = defaults.Fees.TransferFeeBpsZNHB
	}
	if len(cfg.Global.Fees.Assets) == 0 {
		cfg.Global.Fees.Assets = append([]FeeAsset{}, defaults.Fees.Assets...)
	}

	if !meta.IsDefined("global", "Loyalty", "Dynamic", "TargetBPS") {
		cfg.Global.Loyalty.Dynamic.TargetBPS = defaults.Loyalty.Dynamic.TargetBPS
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "MinBPS") {
		cfg.Global.Loyalty.Dynamic.MinBPS = defaults.Loyalty.Dynamic.MinBPS
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "MaxBPS") {
		cfg.Global.Loyalty.Dynamic.MaxBPS = defaults.Loyalty.Dynamic.MaxBPS
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "SmoothingStepBPS") {
		cfg.Global.Loyalty.Dynamic.SmoothingStepBPS = defaults.Loyalty.Dynamic.SmoothingStepBPS
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "CoverageMax") {
		cfg.Global.Loyalty.Dynamic.CoverageMax = defaults.Loyalty.Dynamic.CoverageMax
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "CoverageLookbackDays") {
		cfg.Global.Loyalty.Dynamic.CoverageLookbackDays = defaults.Loyalty.Dynamic.CoverageLookbackDays
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "DailyCapPctOf7dFees") {
		cfg.Global.Loyalty.Dynamic.DailyCapPctOf7dFees = defaults.Loyalty.Dynamic.DailyCapPctOf7dFees
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "DailyCapUSD") {
		cfg.Global.Loyalty.Dynamic.DailyCapUSD = defaults.Loyalty.Dynamic.DailyCapUSD
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "YearlyCapPctOfInitialSupply") {
		cfg.Global.Loyalty.Dynamic.YearlyCapPctOfInitialSupply = defaults.Loyalty.Dynamic.YearlyCapPctOfInitialSupply
	}
	if strings.TrimSpace(cfg.Global.Loyalty.Dynamic.PriceGuard.PricePair) == "" {
		cfg.Global.Loyalty.Dynamic.PriceGuard.PricePair = defaults.Loyalty.Dynamic.PriceGuard.PricePair
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "PriceGuard", "TwapWindowSeconds") {
		cfg.Global.Loyalty.Dynamic.PriceGuard.TwapWindowSeconds = defaults.Loyalty.Dynamic.PriceGuard.TwapWindowSeconds
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "PriceGuard", "PriceMaxAgeSeconds") {
		cfg.Global.Loyalty.Dynamic.PriceGuard.PriceMaxAgeSeconds = defaults.Loyalty.Dynamic.PriceGuard.PriceMaxAgeSeconds
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "PriceGuard", "Enabled") {
		cfg.Global.Loyalty.Dynamic.PriceGuard.Enabled = defaults.Loyalty.Dynamic.PriceGuard.Enabled
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "PriceGuard", "MaxDeviationBPS") {
		cfg.Global.Loyalty.Dynamic.PriceGuard.MaxDeviationBPS = defaults.Loyalty.Dynamic.PriceGuard.MaxDeviationBPS
	}
	if strings.TrimSpace(cfg.Global.Loyalty.Dynamic.PriceGuard.FallbackMinEmissionZNHBWei) == "" {
		cfg.Global.Loyalty.Dynamic.PriceGuard.FallbackMinEmissionZNHBWei = defaults.Loyalty.Dynamic.PriceGuard.FallbackMinEmissionZNHBWei
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "PriceGuard", "UseLastGoodPriceFallback") {
		cfg.Global.Loyalty.Dynamic.PriceGuard.UseLastGoodPriceFallback = defaults.Loyalty.Dynamic.PriceGuard.UseLastGoodPriceFallback
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "EnableProRate") {
		cfg.Global.Loyalty.Dynamic.EnableProRate = defaults.Loyalty.Dynamic.EnableProRate
	}
	if !meta.IsDefined("global", "Loyalty", "Dynamic", "EnforceProRate") {
		cfg.Global.Loyalty.Dynamic.EnforceProRate = defaults.Loyalty.Dynamic.EnforceProRate
	}
}

func (cfg *Config) ensureConsensusDefaults(meta toml.MetaData) {
	defaults := defaultConsensusConfig()

	if !meta.IsDefined("consensus", "ProposalTimeout") {
		cfg.Consensus.ProposalTimeout = defaults.ProposalTimeout
	}
	if !meta.IsDefined("consensus", "PrevoteTimeout") {
		cfg.Consensus.PrevoteTimeout = defaults.PrevoteTimeout
	}
	if !meta.IsDefined("consensus", "PrecommitTimeout") {
		cfg.Consensus.PrecommitTimeout = defaults.PrecommitTimeout
	}
	if !meta.IsDefined("consensus", "CommitTimeout") {
		cfg.Consensus.CommitTimeout = defaults.CommitTimeout
	}
	if !meta.IsDefined("consensus", "MinBlockInterval") {
		cfg.Consensus.MinBlockInterval = defaults.MinBlockInterval
	}
}

// PotsoRewardConfig converts the loaded TOML representation into the runtime configuration structure.
func (cfg *Config) PotsoRewardConfig() (potso.RewardConfig, error) {
	rewards := cfg.Potso.Rewards
	result := potso.DefaultRewardConfig()
	result.EpochLengthBlocks = rewards.EpochLengthBlocks
	if cfg.Potso.Weights.AlphaStakeBps > 0 {
		result.AlphaStakeBps = cfg.Potso.Weights.AlphaStakeBps
	} else {
		result.AlphaStakeBps = rewards.AlphaStakeBps
	}
	result.MaxWinnersPerEpoch = rewards.MaxWinnersPerEpoch
	result.CarryRemainder = rewards.CarryRemainder
	result.MaxUserShareBps = cfg.Potso.Abuse.MaxUserShareBps

	result.MinPayoutWei = big.NewInt(0)
	trimmedMin := strings.TrimSpace(rewards.MinPayoutWei)
	if trimmedMin != "" {
		value, ok := new(big.Int).SetString(trimmedMin, 10)
		if !ok {
			return result, fmt.Errorf("invalid MinPayoutWei value: %s", rewards.MinPayoutWei)
		}
		result.MinPayoutWei = value
	}

	result.EmissionPerEpoch = big.NewInt(0)
	trimmedEmission := strings.TrimSpace(rewards.EmissionPerEpoch)
	if trimmedEmission != "" {
		value, ok := new(big.Int).SetString(trimmedEmission, 10)
		if !ok {
			return result, fmt.Errorf("invalid EmissionPerEpoch value: %s", rewards.EmissionPerEpoch)
		}
		result.EmissionPerEpoch = value
	}

	trimmedTreasury := strings.TrimSpace(rewards.TreasuryAddress)
	if trimmedTreasury != "" {
		addr, err := genesis.ParseBech32Account(trimmedTreasury)
		if err != nil {
			return result, fmt.Errorf("invalid TreasuryAddress: %w", err)
		}
		result.TreasuryAddress = addr
	}

	trimmedMode := strings.TrimSpace(rewards.PayoutMode)
	if trimmedMode == "" {
		result.PayoutMode = potso.RewardPayoutModeAuto
	} else {
		result.PayoutMode = potso.RewardPayoutMode(trimmedMode).Normalise()
	}

	if err := result.Validate(); err != nil {
		return result, err
	}
	if err := result.CheckPayoutModeAvailable(); err != nil {
		return result, err
	}
	return result, nil
}

func (cfg *Config) mergeP2PFromTopLevel() {
	if cfg.P2P.MaxPeers == 0 && cfg.MaxPeers > 0 {
		cfg.P2P.MaxPeers = cfg.MaxPeers
	}
	if cfg.MaxPeers == 0 && cfg.P2P.MaxPeers > 0 {
		cfg.MaxPeers = cfg.P2P.MaxPeers
	}
	if cfg.P2P.MaxInbound == 0 && cfg.MaxInbound > 0 {
		cfg.P2P.MaxInbound = cfg.MaxInbound
	}
	if cfg.MaxInbound == 0 && cfg.P2P.MaxInbound > 0 {
		cfg.MaxInbound = cfg.P2P.MaxInbound
	}
	if cfg.P2P.MaxOutbound == 0 && cfg.MaxOutbound > 0 {
		cfg.P2P.MaxOutbound = cfg.MaxOutbound
	}
	if cfg.MaxOutbound == 0 && cfg.P2P.MaxOutbound > 0 {
		cfg.MaxOutbound = cfg.P2P.MaxOutbound
	}
	if cfg.P2P.MinPeers == 0 && cfg.MinPeers > 0 {
		cfg.P2P.MinPeers = cfg.MinPeers
	}
	if cfg.MinPeers == 0 && cfg.P2P.MinPeers > 0 {
		cfg.MinPeers = cfg.P2P.MinPeers
	}
	if cfg.P2P.OutboundPeers == 0 && cfg.OutboundPeers > 0 {
		cfg.P2P.OutboundPeers = cfg.OutboundPeers
	}
	if cfg.OutboundPeers == 0 && cfg.P2P.OutboundPeers > 0 {
		cfg.OutboundPeers = cfg.P2P.OutboundPeers
	}
	if len(cfg.P2P.Bootnodes) == 0 && len(cfg.Bootnodes) > 0 {
		cfg.P2P.Bootnodes = append([]string{}, cfg.Bootnodes...)
	}
	if len(cfg.Bootnodes) == 0 && len(cfg.P2P.Bootnodes) > 0 {
		cfg.Bootnodes = append([]string{}, cfg.P2P.Bootnodes...)
	}
	if len(cfg.P2P.PersistentPeers) == 0 && len(cfg.PersistentPeers) > 0 {
		cfg.P2P.PersistentPeers = append([]string{}, cfg.PersistentPeers...)
	}
	if len(cfg.PersistentPeers) == 0 && len(cfg.P2P.PersistentPeers) > 0 {
		cfg.PersistentPeers = append([]string{}, cfg.P2P.PersistentPeers...)
	}
	if cfg.P2P.RateMsgsPerSec == 0 && cfg.MaxMsgsPerSecond > 0 {
		cfg.P2P.RateMsgsPerSec = cfg.MaxMsgsPerSecond
	}
	if cfg.MaxMsgsPerSecond == 0 && cfg.P2P.RateMsgsPerSec > 0 {
		cfg.MaxMsgsPerSecond = cfg.P2P.RateMsgsPerSec
	}
	if cfg.P2P.Bootnodes == nil {
		cfg.P2P.Bootnodes = []string{}
	}
	if cfg.P2P.PersistentPeers == nil {
		cfg.P2P.PersistentPeers = []string{}
	}
	if cfg.P2P.HandshakeTimeoutMs == 0 {
		cfg.P2P.HandshakeTimeoutMs = 5000
	}
	if cfg.P2P.Burst == 0 {
		cfg.P2P.Burst = 200
	}
	if cfg.P2P.BanScore == 0 {
		cfg.P2P.BanScore = 100
	}
	if cfg.P2P.GreyScore == 0 {
		cfg.P2P.GreyScore = 50
	}
	if cfg.P2P.BanDurationSeconds == 0 && cfg.PeerBanSeconds > 0 {
		cfg.P2P.BanDurationSeconds = cfg.PeerBanSeconds
	}
	if cfg.PeerBanSeconds == 0 && cfg.P2P.BanDurationSeconds > 0 {
		cfg.PeerBanSeconds = cfg.P2P.BanDurationSeconds
	}
	if cfg.P2P.DialBackoffSeconds == 0 {
		cfg.P2P.DialBackoffSeconds = 30
	}
	if cfg.P2P.PEX == nil {
		enabled := true
		cfg.P2P.PEX = &enabled
	}
}

func (cfg *Config) syncTopLevelToP2P() {
	cfg.P2P.MaxPeers = cfg.MaxPeers
	cfg.P2P.MaxInbound = cfg.MaxInbound
	cfg.P2P.MaxOutbound = cfg.MaxOutbound
	cfg.P2P.MinPeers = cfg.MinPeers
	cfg.P2P.OutboundPeers = cfg.OutboundPeers
	cfg.P2P.Bootnodes = append([]string{}, cfg.Bootnodes...)
	cfg.P2P.PersistentPeers = append([]string{}, cfg.PersistentPeers...)
	cfg.P2P.RateMsgsPerSec = cfg.MaxMsgsPerSecond
	if cfg.P2P.HandshakeTimeoutMs <= 0 {
		cfg.P2P.HandshakeTimeoutMs = 5000
	}
	if cfg.P2P.Burst <= 0 {
		cfg.P2P.Burst = 200
	}
	if cfg.P2P.BanScore <= 0 {
		cfg.P2P.BanScore = 100
	}
	if cfg.P2P.GreyScore <= 0 {
		cfg.P2P.GreyScore = 50
	}
	if cfg.P2P.BanDurationSeconds <= 0 {
		cfg.P2P.BanDurationSeconds = cfg.PeerBanSeconds
	}
	if cfg.P2P.BanDurationSeconds <= 0 {
		cfg.P2P.BanDurationSeconds = int((15 * time.Minute).Seconds())
	}
	if cfg.P2P.DialBackoffSeconds <= 0 {
		cfg.P2P.DialBackoffSeconds = 30
	}
	if cfg.P2P.PEX == nil {
		enabled := true
		cfg.P2P.PEX = &enabled
	}
}

// PotsoWeightConfig converts the weights TOML representation into runtime parameters.
func (cfg *Config) PotsoWeightConfig() (potso.WeightParams, error) {
	weights := cfg.Potso.Weights
	result := potso.DefaultWeightParams()
	result.AlphaStakeBps = weights.AlphaStakeBps
	result.TxWeightBps = weights.TxWeightBps
	result.EscrowWeightBps = weights.EscrowWeightBps
	result.UptimeWeightBps = weights.UptimeWeightBps
	result.MaxEngagementPerEpoch = weights.MaxEngagementPerEpoch
	result.MinEngagementToWin = weights.MinEngagementToWin
	result.DecayHalfLifeEpochs = weights.DecayHalfLifeEpochs
	result.TopKWinners = weights.TopKWinners
	if strings.TrimSpace(weights.TieBreak) != "" {
		result.TieBreak = potso.TieBreakMode(strings.TrimSpace(weights.TieBreak))
	}

	trimmedStake := strings.TrimSpace(weights.MinStakeToWinWei)
	if trimmedStake != "" {
		value, ok := new(big.Int).SetString(trimmedStake, 10)
		if !ok {
			return result, fmt.Errorf("invalid MinStakeToWinWei value: %s", weights.MinStakeToWinWei)
		}
		result.MinStakeToWinWei = value
	} else {
		result.MinStakeToWinWei = big.NewInt(0)
	}

	trimmedEarn := strings.TrimSpace(cfg.Potso.Abuse.MinStakeToEarnWei)
	if trimmedEarn != "" {
		value, ok := new(big.Int).SetString(trimmedEarn, 10)
		if !ok {
			return result, fmt.Errorf("invalid MinStakeToEarnWei value: %s", cfg.Potso.Abuse.MinStakeToEarnWei)
		}
		result.MinStakeToEarnWei = value
	} else {
		result.MinStakeToEarnWei = big.NewInt(0)
	}

	result.QuadraticTxDampenAfter = cfg.Potso.Abuse.QuadraticTxDampenAfter
	result.QuadraticTxDampenPower = cfg.Potso.Abuse.QuadraticTxDampenPower

	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

// SwapSettings exposes the swap configuration with defaults applied.
func (cfg *Config) SwapSettings() swap.Config {
	if cfg == nil {
		return swap.Config{}.Normalise()
	}
	return cfg.Swap.Normalise()
}

// Policy converts the governance TOML representation into a runtime proposal policy.
func (cfg GovConfig) Policy() (governance.ProposalPolicy, error) {
	policy := governance.ProposalPolicy{
		VotingPeriodSeconds:            cfg.VotingPeriodSeconds,
		TimelockSeconds:                cfg.TimelockSeconds,
		AllowedParams:                  append([]string{}, cfg.AllowedParams...),
		QuorumBps:                      cfg.QuorumBps,
		PassThresholdBps:               cfg.PassThresholdBps,
		AllowedRoles:                   append([]string{}, cfg.AllowedRoles...),
		BlockTimestampToleranceSeconds: cfg.BlockTimestampToleranceSeconds,

		RejectAmbiguousParamKeysFromUnix: cfg.RejectAmbiguousParamKeysFromUnix,
	}
	amount, err := parseUintAmount(cfg.MinDepositWei)
	if err != nil {
		return policy, fmt.Errorf("invalid MinDepositWei value: %w", err)
	}
	if amount != nil {
		policy.MinDepositWei = amount
	}
	for _, raw := range cfg.TreasuryAllowList {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		addr, err := crypto.DecodeAddress(trimmed)
		if err != nil {
			return policy, fmt.Errorf("invalid TreasuryAllowList entry %q: %w", raw, err)
		}
		bytes := addr.Bytes()
		if len(bytes) != 20 {
			return policy, fmt.Errorf("treasury address must be 20 bytes")
		}
		var entry [20]byte
		copy(entry[:], bytes)
		policy.TreasuryAllowList = append(policy.TreasuryAllowList, entry)
	}
	return policy, nil
}

func parseUintAmount(value string) (*big.Int, error) {
	trimmed := strings.ReplaceAll(strings.TrimSpace(value), "_", "")
	if trimmed == "" {
		return big.NewInt(0), nil
	}
	normalized := trimmed
	var exponent int64
	if idx := strings.IndexAny(normalized, "eE"); idx != -1 {
		expPart := strings.TrimSpace(normalized[idx+1:])
		if expPart == "" {
			return nil, fmt.Errorf("invalid scientific notation")
		}
		expValue, err := strconv.ParseInt(expPart, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid scientific notation")
		}
		exponent = expValue
		normalized = strings.TrimSpace(normalized[:idx])
	}
	normalized = strings.TrimPrefix(normalized, "+")
	if strings.HasPrefix(normalized, "-") {
		return nil, fmt.Errorf("amount must not be negative")
	}
	parts := strings.Split(normalized, ".")
	if len(parts) > 2 {
		return nil, fmt.Errorf("invalid amount format")
	}
	integerPart := parts[0]
	fractionalPart := ""
	if len(parts) == 2 {
		fractionalPart = parts[1]
	}
	digits := integerPart + fractionalPart
	if digits == "" {
		return big.NewInt(0), nil
	}
	if !isDigitString(digits) {
		return nil, fmt.Errorf("invalid amount format")
	}
	fracLen := len(fractionalPart)
	for fracLen > 0 && len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		fracLen--
	}
	digits = strings.TrimLeft(digits, "0")
	totalExponent := exponent - int64(fracLen)
	if totalExponent < 0 {
		return nil, fmt.Errorf("amount must be an integer")
	}
	if digits == "" {
		digits = "0"
	}
	if totalExponent > 0 {
		digits += strings.Repeat("0", int(totalExponent))
	}
	amount := new(big.Int)
	if _, ok := amount.SetString(digits, 10); !ok {
		return nil, fmt.Errorf("invalid amount value")
	}
	return amount, nil
}

// ParseAmount parses the configured amount format used across NHBChain config
// files and returns the integer base-unit value.
func ParseAmount(value string) (*big.Int, error) {
	return parseUintAmount(value)
}

func isDigitString(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func ensureKeystore(configPath string, cfg *Config, resolvePassphrase func() (string, error)) error {
	keystorePath := cfg.ValidatorKeystorePath
	if keystorePath == "" {
		keystorePath = defaultKeystorePath(configPath)
	}

	if _, err := os.Stat(keystorePath); os.IsNotExist(err) {
		if resolvePassphrase == nil {
			return fmt.Errorf("validator keystore %s does not exist and no passphrase was provided", keystorePath)
		}
		passphrase, perr := resolvePassphrase()
		if perr != nil {
			return perr
		}
		if strings.TrimSpace(passphrase) == "" {
			return fmt.Errorf("validator keystore passphrase is required to create %s", keystorePath)
		}
		key, genErr := crypto.GeneratePrivateKey()
		if genErr != nil {
			return genErr
		}
		if err := crypto.SaveToKeystore(keystorePath, key, passphrase); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Keep the resolved path in memory only. The default is derived from the
	// config path on every Load, so there is nothing to record, and writing the
	// struct back here would store every key the operator left out as an
	// explicit zero before the defaults below have been applied: the next Load
	// would then take those zeros as the operator's own values.
	cfg.ValidatorKeystorePath = keystorePath

	return nil
}

// createDefault creates and saves a default configuration file.
func createDefault(path string, passphrase string) (*Config, error) {
	if strings.TrimSpace(passphrase) == "" {
		return nil, fmt.Errorf("validator keystore passphrase is required to initialise %s", path)
	}
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}

	keystorePath := defaultKeystorePath(path)
	if err := crypto.SaveToKeystore(keystorePath, key, passphrase); err != nil {
		return nil, err
	}

	cfg := &Config{
		ListenAddress:            ":6001",
		RPCAddress:               ":8080",
		DataDir:                  "./nhb-data",
		GenesisFile:              "",
		AllowAutogenesis:         false,
		NetworkName:              "nhb-local",
		Bootnodes:                []string{},
		PersistentPeers:          []string{},
		MaxPeers:                 64,
		MaxInbound:               64,
		MaxOutbound:              64,
		MinPeers:                 32,
		OutboundPeers:            16,
		PeerBanSeconds:           int((60 * time.Minute).Seconds()),
		ReadTimeout:              int((90 * time.Second).Seconds()),
		WriteTimeout:             int((5 * time.Second).Seconds()),
		RPCReadHeaderTimeout:     int((10 * time.Second).Seconds()),
		RPCReadTimeout:           int((15 * time.Second).Seconds()),
		RPCWriteTimeout:          int((15 * time.Second).Seconds()),
		RPCIdleTimeout:           int((120 * time.Second).Seconds()),
		RPCMaxTxPerWindow:        5,
		RPCMaxTxPerIP:            5,
		RPCMaxTxPerIdentity:      5,
		RPCMaxTxPerChain:         5,
		RPCMaxTxPerIdentityChain: 5,
		RPCRateLimitWindow:       int((time.Minute).Seconds()),
		MaxMsgBytes:              1 << 20,
		MaxMsgsPerSecond:         32,
		ClientVersion:            "nhbchain/node",
	}
	cfg.Mempool.MaxTransactions = DefaultMempoolMaxTransactions
	cfg.NetworkSecurity.StreamQueueSize = defaultStreamQueueSize
	cfg.NetworkSecurity.RelayDropLogRatio = defaultRelayDropLogRatio
	cfg.P2P = P2PSection{
		NetworkIDRaw:       "18346390202490284624",
		MaxPeers:           64,
		MaxInbound:         60,
		MaxOutbound:        30,
		MinPeers:           32,
		OutboundPeers:      16,
		Bootnodes:          []string{},
		PersistentPeers:    []string{},
		BanScore:           100,
		GreyScore:          50,
		RateMsgsPerSec:     50,
		Burst:              200,
		HandshakeTimeoutMs: 5000,
		BanDurationSeconds: int((60 * time.Minute).Seconds()),
		DialBackoffSeconds: 30,
	}
	enabledPEX := true
	cfg.P2P.PEX = &enabledPEX
	cfg.Potso.Rewards = PotsoRewardsConfig{
		MinPayoutWei:     "0",
		EmissionPerEpoch: "0",
		CarryRemainder:   true,
		PayoutMode:       string(potso.RewardPayoutModeAuto),
	}
	cfg.Governance = GovConfig{
		MinDepositWei:       "1000e18",
		VotingPeriodSeconds: 604800,
		TimelockSeconds:     172800,
		QuorumBps:           2000,
		PassThresholdBps:    5000,
		AllowedParams:       append([]string(nil), defaultAllowedGovernanceParams...),
	}
	cfg.Global = defaultGlobalConfig()
	cfg.Consensus = defaultConsensusConfig()
	cfg.ValidatorKeystorePath = keystorePath
	cfg.syncTopLevelToP2P()

	if err := persist(path, cfg); err != nil {
		return nil, err
	}

	// Return what the next start reads from the file just written rather than
	// the literal above: Load fills in every unset default (subscriptions,
	// swap, POTSO weights, ...), and a first run without them panicked at
	// startup while every later run was fine.
	return Load(path, WithKeystorePassphrase(passphrase))
}

func persist(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewEncoder(f).Encode(cfg)
}

func defaultKeystorePath(configPath string) string {
	dir := filepath.Dir(configPath)
	if dir == "." || dir == "" {
		dir = ""
	}
	return filepath.Join(dir, "validator.keystore")
}
