# Consensus Invariants

This page lists the consensus rules that the code enforces today, with the
place each one is enforced. It is not a design wish list: a rule appears here
only if the code checks it. When changing consensus-critical code, keep these
checks intact.

## Block acceptance

Enforced in `core/node.go` (`commitBlock`, called with the synced-block flag from
`commitSyncedBlock` and without it from `CommitBlock`; and `ValidateBlock`):

1. **Size limit.** `rejectOversizedBlock` runs first, in both `ValidateBlock` and
   `commitBlock`: a block with more transactions than `[global.Blocks] MaxTxs`
   (when that is positive) is refused (`block exceeds max transaction count`).
2. **Quorum certificate on synced blocks.** For blocks that arrive by peer sync
   (not produced in the local BFT round) with height above
   `QuorumCertActivationHeight`, `QuorumCert.Verify`
   (`core/types/vote.go`) must pass against the validator set of the parent
   height (`validatorSetAtHeight(height-1)`; if that set cannot be resolved the
   block is rejected). Verification requires: the certificate's block hash
   equals the recomputed header hash; every signature is 65 bytes, recovers to
   the claimed validator, and that validator has positive power in the set;
   duplicates are counted once; and the signed power is at least
   `types.QuorumThreshold(total)` = `floor(2 * total / 3) + 1`, strictly more
   than two thirds. The check is off when the activation height is `0` or unset
   (`cmd/nhb/main.go` only applies a value greater than zero, and the node's own
   default is "never verify"). The shipped `config.toml` sets it to `0`.
3. **Transaction root.** `ComputeTxRoot(transactions)` must equal
   `Header.TxRoot` (`tx root mismatch`).
4. **Timestamp window.** `validateBlockTimestamp` requires the block timestamp to
   be no earlier than the previous block's timestamp. For a block the node
   produced or is voting on live (not the historical sync path) it must also be
   no later than `now + tolerance`, where the tolerance is
   `[governance] BlockTimestampToleranceSeconds` (5 in `config.toml` and the
   default when the key is absent or zero).
5. **Contiguous heights.** A block is accepted only if
   `Header.Height == chain height + 1`. A block at or below the current height is
   accepted only if its header hash equals the stored block's header hash
   (idempotent replay, answered with no error); otherwise the error is
   `block height mismatch: got N want M`.
6. **Execution graph root.** The block's transactions are put in the canonical
   dependency order (`computeDependencyGraph`); a header that carries an
   `ExecutionGraphRoot` that differs from the computed one is refused
   (`execution graph root mismatch`).
7. **State root.** `ValidateBlock` and `commitBlock` re-execute the block on a
   copy of state and require `Header.StateRoot` to equal the resulting pending
   root (`state root mismatch: header=... pending=...`). Execution of a block's
   transactions, the end-of-block lifecycle (`ProcessBlockLifecycle`) and
   evidence processing all run inside that computation.

## Transaction execution

Enforced in `core/state_transition.go` (`executeTransaction`):

1. **Chain ID.** `types.IsValidChainID(tx.ChainID)` must hold.
2. **Expiry.** For transaction types above `0`, `MaxBlockHeight` (if set) must
   not be exceeded at the current block height, and `IntentExpiry` (if set and
   there is no `IntentRef`) must not be before the block timestamp. Both use
   block values, not wall-clock time.
3. **Nonce.** For every type that has a sender, `tx.Nonce` must equal the
   account nonce exactly (`ErrNonceTooLow` / `ErrNonceTooHigh`,
   `validateSenderAccount`).
4. **Events are transactional.** When a transaction returns an error, the
   events it appended are discarded. The exceptions are rejections that are
   themselves reported by an event: the two transfer pauses, a refused
   sponsorship and a paused staking module (`ErrTransferZNHBPaused`,
   `ErrTransferNHBPaused`, `ErrSponsorshipRejected`, `ErrStakePaused`).
5. **Determinism inputs.** Values that end up in state come from the block, not
   the local clock. For example a heartbeat with no explicit timestamp uses the
   block timestamp (`applyHeartbeat`, `core/state_transition.go`).

## BFT engine

Enforced in `consensus/bft/bft.go` and `consensus/bft/round_sync.go`:

1. **Quorum is strictly more than two thirds.** A commit needs precommits with
   power at least `types.QuorumThreshold(totalVotingPower)` =
   `floor(2 * total / 3) + 1`, computed over the validator set the engine holds
   for the height (`hasTwoThirdsPowerLocked`, which calls `types.HasQuorum`,
   `core/types/quorum.go`). Exactly two thirds is not enough, so with three
   equal validators all three have to sign. The same helper is used by
   `QuorumCert.Verify`, by the polka-proof check and by `core/sync`'s manifest
   and header proofs. An empty validator set never has a quorum.
2. **Validator set refresh.** The engine reloads `node.GetValidatorSet()` and
   recomputes total power when a new round starts (`startNewRound`) and after a
   commit (`commit`). A change made to the validator set while executing block
   `H` is therefore first used for height `H+1`.
3. **Height alignment.** The engine never proposes for a height at or below the
   node's committed height, and every height starts in round 1 (see
   [BFT height sync](bft-height-sync.md)).
4. **Round bounds.** The engine adopts a later round only when validators
   holding at least a third of the voting power have been seen there (a group of
   that size has an honest validator in it while less than a third is faulty,
   and the validators outside it cannot make a quorum without it); it is never
   in a round above `math.MaxInt32`, and it keeps future-round messages only
   within 8 rounds of its own (`round_sync.go`).
5. **Lock discipline.** A round timeout does not clear the proof-of-lock state;
   lock, valid block and polka history are reset only when the height changes
   (`startNewRound`, `resetLockStateLocked`), and the lock is persisted to
   `<DataDir>/polc_lock.json` and restored only for the same height.
6. **No conflicting vote.** The one function that signs a vote (`createVote`)
   refuses to sign a different value for a vote type in the (height, round) of the
   last vote it signed, and refuses an earlier round or height. The record is
   written to `<DataDir>/bft_sign_state.json` before the vote is signed, and a
   vote whose record cannot be written is not signed
   (`consensus/bft/sign_state.go`).

## Validator set

Enforced in `core/state_transition.go` and `core/epochs.go`:

1. **Eligibility.** An address is in `EligibleValidators` only if
   `ValidatorRegistered`, it is not delegating its own stake to another
   validator, and its total `Stake` (which includes ZNHB delegated in by other
   wallets) is at least `staking.minimumValidatorStake` (default 10,000 ZNHB).
   `setAccount` recomputes this every time the account is written.
2. **Selection timing.** The active set is recomputed only when
   `height % epoch length == 0` and height is not `0`
   (`ProcessBlockLifecycle`). The epoch length used is
   `epoch.DefaultConfig().Length` = 100; rotation is disabled, so all eligible
   accounts with a fresh heartbeat are included.
3. **Heartbeat freshness.** An account joins the set only if its last heartbeat
   is at most `max(5 x heartbeat interval, 15 minutes)` older than the boundary
   block time and not more than 2 minutes ahead of it
   (`validatorReadyForActivation`, `core/epochs.go`).
4. **Liveness fallback.** If no account qualifies, `fallbackValidatorSet` builds
   a set from the previous active set, eligible addresses and past epoch
   selections, still requiring registration, minimum stake, not delegated away,
   and at least one heartbeat ever (it skips the freshness window).
   `ensureValidatorSetLiveness` applies the same fallback at node start-up when
   the loaded set is empty (`core/node.go`).
5. **Removal.** An account that stops satisfying rule 1 is removed from the
   active set in the same transaction (`setAccount`).

## ZNHB pool ledger

`CheckZNHBSupplyInvariant` (`core/state_transition.go`) runs every block from
`ProcessBlockLifecycle`. When an admin/treasury wallet is configured and the
pools have been bootstrapped, it requires
`SalePool + RewardPool == admin BalanceZNHB + LockedZNHB + pending unbonds
(+ governance escrow)`. A violation is returned as a hard error. It checks the
internal consistency of the two pool sub-ledgers; it does not check any fixed
total supply.

Every credit or debit of the treasury wallet is booked into the pool ledger in the
same state transition, so an ordinary transaction cannot break the invariant:
`executeTransaction` books what a transaction of a tracked type leaves unmirrored
into the Reward Pool (`treasuryZNHBFlowTracked`, `bookTreasuryPoolMovement`,
`core/znhb_treasury_pool.go`); the same is done around subscription billing and
POTSO reward payouts in `ProcessBlockLifecycle` (`withTreasuryPoolBooking`), and
around the slash of a validator's stake. A transaction that would move more ZNHB
off the treasury wallet than the Reward Pool holds is rejected with
`ErrTreasuryRewardPoolInsufficient`.

## Other checks

- **Evidence.** `TxTypeSubmitEvidence` (0x4C) transactions are applied by
  `applySubmitEvidenceTransaction` (`core/potso_evidence_tx.go`). The
  transaction is an ordinary signed transaction: its sender must be the report's
  own `Reporter` and must have at least the governed minimum validator stake in
  its own `LockedZNHB` (`ErrEvidenceReporterNotBonded`). The evidence is
  validated by `ValidateEvidence`, and a report of an offense that is already
  recorded is refused. Held records are capped at `evidence.MaxPendingRecords`
  (256), at most `evidence.MaxRecordsPerReporter` (4) per reporter
  (`consensus/potso/evidence/types.go`), and records whose offense is older than
  `evidence.DefaultMaxAgeBlocks` (8640 blocks) are pruned; older evidence is
  rejected. The penalty for each recorded report is applied in the block that
  records it, run inside block validation (`processPendingEvidenceForState`,
  `core/node.go`). The slash is bounded by the offender's recorded stake and its
  locked ZNHB, and the forfeited ZNHB goes to the admin wallet (or a fixed module
  address on a chain without one). The unsigned `potso_submitEvidence` RPC method
  is removed.
- **Sender quota.** Every signed transaction is subject to a per-sender
  requests-per-minute check at mempool admission, using the `[global.Quotas.Trade]`
  value (`applySenderQuotaLocked`, `core/node.go`, described in the
  `config.toml` comment on that section).

## Lending rules already live without an activation height

Four changes to the lending engine reached the running line with no activation height
(ledger PL-R5T-51). They cannot be gated retroactively: the chain has run them. They are
recorded here exactly, and pinned byte for byte by tests, so that the next change to any of
them cannot arrive the same way.

1. **Accrual basis.** Interest accrues by the block time that has passed since the market's
   last refresh (`Engine.elapsedSeconds`, `Engine.markAccrued`), over a year of 31,536,000
   seconds. The earlier release charged by block count, so at a block every two seconds the
   stated annual rate is charged where the earlier basis charged half of it. A market that has
   never been stamped accrues its first step at one second per block
   (`legacySecondsPerBlock`) and is stamped by it. Pinned by
   `native/lending/live_rules_golden_test.go` (the indexes, totals and reserve share after one day
   of block time and after 100 unstamped blocks).
2. **Market record.** `storedLendingMarket` (`core/state/manager.go`) ends in the optional field
   `LastUpdateTimestamp`. While it is zero the record is byte-identical to the earlier
   release's; once it is not, the record carries the empty optional fields before it and an
   older binary cannot decode it. A stamp is first written by the first lending engine use of a
   pool. Pinned by `core/state/lending_live_rules_golden_test.go` (the record with and without
   the stamp). **After the first lending engine transaction, do not roll back to an earlier
   release and do not run the public `main` branch** as it stands: either reads or writes
   another state.
3. **Sweep mark.** The first time an engine is built for a pool, the key
   `lending/legacy-reconciled/<pool>` is written with the value `true` (the sweep of legacy
   positions on plain accounts). A reference-price transaction and the block lifecycle never
   write it. Pinned by the same file and by `core/lending_legacy_sweep_test.go`.
4. **Seizure pricing.** Collateral seized in a liquidation is the repaid debt plus the bonus,
   converted at the oracle price and rounded down, one to one when the market has no price
   (`CollateralForDebtValue`); the eligibility check values collateral at the same price
   (`OracleAdjustedCollateralValue`). Pinned by `native/lending/live_rules_golden_test.go`.

The settings of the lending work (`LendingAccountingFixesActivationHeight`,
`LendingPoolLimitsActivationHeight`, `LendingRefPriceGuardsActivationHeight`,
`LendingRiskActivationHeight`, `LendingFeeWithdrawActivationHeight`) do not change these four:
with them off, the pinned values are produced.

**The gate for the next change to any of the four.** (a) A new setting, named for the rule,
`Lending<Rule>ActivationHeight`, whose zero value reproduces the pinned behaviour at every
height; (b) the engine receives the choice from the block height
(`StateProcessor.lendingEngine`), never from anything a transaction carries; (c) a market
field is added only as one more optional trailing field, and nothing writes it before the
height; (d) the tests above stay unchanged and keep passing with the setting off, and new tests
pin the new values from the height; (e) the setting is documented here, pinned off in
`config.toml`, `cmd/nhb-snapshot/pins.go` and `tests/config`, carried by
`StateProcessor.Copy` and the node's rebuild path, and replayed against real blocks before it is
turned on. A test above that fails is never fixed by editing its numbers.

### The intent registry and the lending transactions (PL-R5T-48)

The intent registry is one global namespace (`pos/intent/<ref>`, `core/state/intent_registry.go`):
`ApplyTransaction` validates and consumes an `IntentRef` for any transaction type and any
sender that carries one, so anyone able to sign a transaction can consume a reference that
someone else chose (a payment intent's reference, for one). That is unchanged and is not
changed by this work. On the public `main` branch two senderless lending fee-withdrawal
types used their public signing hash as the `IntentRef` and relied on the registry as their
only replay guard, so a stranger's transaction carrying that reference killed the
administrator's authorisation. **That finding is not applicable to the running line**,
checked on the live release (a649ca5), `release/final-r11` and `release/final-r12`: none of
the two types exists there, and no lending source file mentions the registry or an
`IntentRef`. The fee-withdrawal transaction of this line (`TxTypeLendingWithdrawProtocolFees`)
is an ordinary signed transaction whose replay guard is the sender's nonce, so the finding
cannot arise through it either. `core/lending_intent_registry_scope_test.go` fails the day a
lending file starts to depend on the registry, or a lending type becomes senderless (other than
the reference price, which carries its own signer quorum and a monotonic timestamp). If the
senderless design of `main` is ever brought here, its own registry namespace (commit
`2036ef0` on that branch) must come with it.

## Validation checklist for consensus changes

- Add tests next to the change (`core/*_test.go`, `consensus/bft/*_test.go`).
  The repository has determinism, fuzz and network bug-check targets in the
  `Makefile`: `bugcheck-determinism`, `bugcheck-fuzz`, `bugcheck-network`,
  `bugcheck-race`.
- `QuorumCertActivationHeight` must be identical on every validator
  (`config/config.go`, `QuorumCertActivationHeight`).
- `RewardsByStakeFromEpoch` must be identical on every validator and on every node
  that replays blocks, and in force before the epoch it names begins: it decides
  who an epoch's reward is credited to, so another value is another state root
  (`config/config.go`, `core/rewards_logic.go` `distributeStakeWeighted`).
- `POSSweepSkipActivationHeight` must be identical on every validator
  (`config/config.go`, `POSSweepSkipActivationHeight`). Unlike the setting above
  it changes the state a block leaves behind (the POS expiry sweep, see
  `docs/specs/pos-lifecycle.md`), so it is changed only with all validators
  restarted together, to a height above the chain tip.
- `[swap] PayoutReceiptActivationHeight` must be identical on every validator
  (`native/swap/oracle.go`, `Config.PayoutReceiptActivationHeight`). Unlike the
  setting above it changes the state root of blocks that carry a swap payout
  receipt, so it is chosen as a height ahead of the tip.
- `GovCreditPreservationActivationHeight` and `FeeRouteAliasActivationHeight` must be
  identical on every validator (`config/config.go`). Each decides which of two
  results a transaction leaves in state (a governance finalize or execute that keeps
  a credit the engine wrote to the sender; an NHB domain fee whose route wallet, or
  whose buyback accrual share's account, is the sender or recipient: PL-R1-FEEROUTE
  and PL-R5T-59), so each is changed only with all validators restarted
  together, to a height above the chain tip. The state processor copies carry both
  (`StateProcessor.Copy`): a copy that dropped one would apply the fix from height 0.
- `LendingFixedTermIDActivationHeight` must be identical on every validator
  (`config/config.go`). It changes the key a fixed-term loan or deposit is written
  under (`lendingFixedTermRecordID`, `core/lending_native.go`: the owner folded into
  the transaction hash) and makes an origination onto an existing record fail, so it
  is changed only with all validators restarted together, to a height above the
  chain tip. Records written before the height keep their ID; every lookup goes
  through an ID that was stored.
- `NHBSupplyGenesisCorrectionActivationHeight` must be identical on every validator
  (`config/config.go`). From that height, in the first block that reaches it, the
  tracked NHB supply counter (`token/supply/NHB`, served by `nhb_getTotalSupply`) is
  raised once by the part of the genesis allocation it never counted (90,000 NHB on
  the live network; `StateProcessor.ReconcileNHBGenesisShortfallOnce`), on that
  network's chain id only. It changes the state root of that one block and every
  block after it, so it is changed only with all validators restarted together, to a
  height above the chain tip. No balance changes. The correction runs at the same
  call sites as the genesis seed (block builder, validator, commit path, containment
  re-build, lifecycle); a test fails if one of them is left out.
- `TransferMonthlyFreeTierActivationHeight` and `TransferMonthlyFreeTierTxPerMonth` must
  be identical on every validator (`config/config.go`). From the height, a sender's
  first N plain transfers each UTC calendar month (NHB and ZNHB share one counter,
  `fees/transfer-gas/txcount/<month>/<sender>`) pay no transfer fee, on top of the
  existing spend allowance; a paymaster-carried transfer is not counted. It decides
  what a transfer debits and what the fee collector is credited, so it is changed only
  with all validators restarted together, to a height above the chain tip. The state
  processor copies carry both values (`StateProcessor.Copy`).
- `LoyaltyBudgetFloorActivationHeight` and `LoyaltyBudgetFloorZNHBWei` must be identical
  on every validator (`config/config.go`). From the height the base-reward daily
  budget is at least the floor, limited to the daily USD cap
  (`Manager.GetRemainingDailyBudgetZNHBWithFloor`); both settlement paths and the
  `nhb_getLoyaltyBudgetStatus` figure use it. It decides how much of the day's base
  rewards is paid, so it is changed only with all validators restarted together, to a
  height above the chain tip.
- `PotsoGovernanceWeightActivationHeight` must be identical on every validator
  (`config/config.go`). From the height the stored weight snapshot that governance
  reads (`SnapshotPotsoWeights`, used by `CastVote`) is built by
  `StateProcessor.governanceWeightSnapshot` instead of being the snapshot of the
  rewards: delegated stake is counted for the delegator, not the validator (total
  unchanged), and a snapshot is stored every epoch. POTSO rewards are paid from the
  snapshot of the rewards, which does not change. It changes who may vote and with
  what power, so it is changed only with all validators restarted together, to a
  height above the chain tip.
- `ValidatorFlagNonceActivationHeight` must be identical on every validator
  (`config/config.go`). From the height a pure validator registration (stake with
  `registerValidator` and no value) and a pure deregistration (unstake with
  `deregisterValidator` and no value) persist the sender nonce plus one, also when the
  flag is already in the requested state (`applyStake`, `applyUnstake`); before it they
  never advance the nonce, as in every block already committed. Documented onboarding
  signs `register-validator 0` and then sends heartbeats at the same nonce, so a
  validator that registers after the height must sign its first heartbeat at the next
  nonce. It decides whether a transaction is valid, so it is changed only with all
  validators restarted together, to a height above the chain tip.
- `MintRunningChainIDActivationHeight` must be identical on every validator
  (`config/config.go`). From the height a mint voucher is also accepted when its
  `chainId` is the running chain's own id (`StateProcessor.mintVoucherChainIDAccepted`,
  used by `applyMintTransaction` and by the node's admission check), next to the Phase E
  id `core.MintChainID` that is the only one accepted before it. The Phase E id, which
  also guards the two one-shot supply repairs, stays valid for vouchers until
  `MintPhaseEChainIDRetireHeight`. It decides whether a transaction is valid, so it is
  changed only with all validators restarted together, to a height above the chain tip.
- `MintPhaseEChainIDRetireHeight` must be identical on every validator
  (`config/config.go`). From the height a mint voucher naming the Phase E id
  `core.MintChainID` is refused with `ErrMintInvalidChainID` and only the running chain's
  id is accepted (`StateProcessor.mintVoucherChainIDAccepted`); it also switches the
  running-id acceptance of `MintRunningChainIDActivationHeight` on if that is later or
  zero. It decides whether a transaction is valid, so it is changed only with all
  validators restarted together, to a height above the chain tip, after the voucher
  signer has moved to the running id.
- `TxNetworkChainIDActivationHeight` must be identical on every validator
  (`config/config.go`). From the height a transaction's envelope `ChainID` must equal the
  network's own chain id (`Node.ChainID()`, the first 8 bytes of the genesis hash) and the
  legacy constant `types.NHBChainID()` (`0x4e4842`) is refused with `ErrInvalidChainID`
  (`StateProcessor.txChainIDAccepted`, used by `executeTransaction`, by admission in
  `Node.validateTransaction` and by the RPC pre-check). Below the height only the legacy
  constant is accepted. The id is part of the V3 signing hash, so the change makes every
  signature network-specific. Node-built transactions use `Node.txChainIDForNextBlock`;
  `nhb_getNetworkStats` reports the id a transaction must carry (`txChainId`). It decides whether a
  transaction is valid, so it is changed only with all validators restarted together, to a
  height above the chain tip, after every signer reads the id from `nhb_getNetworkStats`.
- `VoteChainBindingActivationHeight` must be identical on every validator
  (`config/config.go`). A consensus vote at or above the height is signed and verified
  over `sha256("nhb-vote-binding-v1" | len(genesis) | genesis hash | vote payload)`
  (`types.VoteBinding.Digest`) instead of `sha256(vote payload)`: by the BFT engine for
  its own votes, for votes received and polka proofs (`consensus/bft`), by
  `QuorumCert.VerifyBound` for a synced block's certificate and by
  `evidence.ValidateEvidenceBound` for the two votes of an equivocation proof. Below the
  height only the legacy digest verifies, so every committed block and any proof about
  an earlier height are judged as before. It decides which blocks and proofs are valid,
  so it is changed only with all validators restarted together, to a height above the
  chain tip. Until it is set, never reuse a validator key across networks or relaunches.
- `SlashEligibilityRefreshActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`, which the node, the state
  processor and its copies all carry). From the height, in the block that slashes a
  validator for equivocation (`processPendingEvidence`), its eligibility entry and its
  place in the validator set are recomputed through `setAccount`
  (`refreshValidatorEligibility`): the slasher debits the offender's stake directly and
  until then the offender kept the voting power, the eligibility basis and the POTSO
  reward and governance weight it had, until its account was next written. An address
  that is in neither map is not touched. It changes the validator set and so what every
  later block is voted on by, so it is changed only with all validators restarted
  together, to a height above the chain tip.
- `SlashPendingUnbondsActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`). From the height a proven
  equivocation continues, after the offender's locked balance, into the offender's own
  unclaimed unbonds (`ValidatorSlasher.ReachingPendingUnbonds`: the entries made out of
  its stake in itself, oldest first, up to the amount asked), and the slash is sized on
  its stake plus those unbonds. ZNHB moved into an unbond before the report landed was
  out of reach before (an unbond lasts 7 days by default, the evidence window is 8,640
  blocks) and the offense was still marked penalised with a slash of zero. What is
  taken lands on the same treasury as any slash, total ZNHB is unchanged, and the
  validator's recorded stake is lowered only by the locked part. It does NOT reach stake
  other wallets delegated to the validator: what to do about that (slash the delegators
  pro rata, or deregister the validator and bar it from registering again, or require an
  own bond) is a policy decision this rule does not make. Changed only with all
  validators restarted together, to a height above the chain tip.
- `EvidenceIntakeGuardActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`). From the height the evidence
  intake (`applySubmitEvidenceTransaction`) refuses the reports that fill its 256 slots
  without a proof behind them: a DOWNTIME or INVALID_BLOCK_PROPOSAL report
  (`ErrEvidenceTypeNotAccepted`, dropped by the block builder: it can never become valid);
  an EQUIVOCATION report whose offender holds less than the minimum validator stake as
  staking `Stake` plus its own pending unbonds (`ErrEvidenceOffenderNotBonded`, kept and
  offered again: the offender can bond more); and a report whose reporter's staking
  `Stake`, not its `LockedZNHB`, is under the minimum (`ErrEvidenceReporterNotBonded`; a
  point-of-sale hold raises `LockedZNHB` and can be voided at once). It changes which
  transactions are valid, so it is changed only with all validators restarted together,
  to a height above the chain tip. Effect on who may report: a wallet that only delegates
  to a validator has `Stake` zero, so only validators and self-stakers can report.
- `StakeAPRFromStateActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`). From the height the reward
  rate used when a stake, unstake or claim executes (`advanceStakeRewards`, the claim
  events) is the on-chain `staking.aprBps` read at execution (`stakeAPRFromParam`; the
  fixed default 1250 when none is stored or the stored value is empty, not a whole
  number, or wider than 32 bits), never the value `SyncStakingParams` cached at process
  start from the node's own config.toml. The rate the reward index was last settled at is
  read from the persisted reward record in the trie, never from the process, and when it
  differs from the chain's the index is first advanced to the current block at the OLD
  rate (`settleStakeAPRChange`, also run in the block a governance execution changes the
  parameter), so no interval is repriced. Before the height nothing changes, including
  the start-up assignment. It changes the reward index every later stake transaction
  reads, so it is changed only with all validators restarted together, to a height above
  the chain tip.
- `ValidatorAdmissionGuardActivationHeight` and `ValidatorAdmissionProbationSeconds` must
  be identical on every validator (`config/config.go`; carried as `core.ValidatorRules`;
  a probation of zero leaves the guard without effect). From the height, at each epoch
  boundary (`applyValidatorSelection`, both the stake-ordered and the rotating branch), a
  candidate that is not in the validator set and is not ESTABLISHED (a newcomer) is
  admitted only while the established validators hold strictly more than twice the power
  of the newcomers (the newcomers already admitted and still inside their probation, plus
  this one), i.e. while the newcomers together stay below a third of the set and the
  established validators keep their quorum without them (`admitValidators`,
  `core/validator_admission.go`). A validator is established once it has been in the set
  for the whole probation; the validators in the set when the guard first runs are
  established for good, and so is one that was established and drops out (a stale
  heartbeat) and returns. Newcomers are served by registration time, then address. A
  deferred newcomer stays eligible and is considered again at the next boundary. The rule
  never removes a member, and with no established validator in the set (a young chain)
  everyone is admitted. The admission times are kept in the state (`validator-admitted-at`,
  written only once the guard runs). What this means for the live set: with the two
  validators at 10,010,000 ZNHB each, newcomers together stay below 10,010,000 until they
  have been in the set for the probation, so a validator larger than that cannot join
  until the established power grows (who may join at what size is a policy choice, see the
  W3 report). Changed only with all validators restarted together, to a height above the
  chain tip.
- `EscrowEnvelopeChainBindingActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`). From the height the signed
  payload of a delegated escrow create, release, refund or dispute
  (`applyDelegatedCreateEscrow`, `applyDelegatedEscrowAction`) and the decision of an
  arbitration quorum (`applyArbitrate`) must carry the network's own chain id as `chainId`,
  a canonical decimal string equal to the id `nhb_getNetworkStats` reports as
  `networkChainId` (`StateProcessor.swapVoucherChainID`), and one that does not is refused
  before the escrow engine looks at it (`requireEscrowEnvelopeChain`). The signature covers
  the payload bytes, so the chain id is signed with the rest and a relayer cannot add it.
  An escrow id is a hash of payer, payee, meta hash and nonce and so is the same on every
  network that holds the escrow: before the height a signature made for one network
  releases, refunds, disputes or creates the mirror escrow on any other (ledger PL-EXT-07).
  Before the height the payloads are accepted as they are, and the key is ignored if
  present, so wallets can start signing it ahead of the height. An unset network id
  (zero) accepts nothing from the height on. Not covered: the milestone project
  signatures, which nothing in the tree reaches but tests. The escrow gateway adds the key
  to the envelopes it builds when `ESCROW_GATEWAY_ENVELOPE_CHAIN_ID` is set. Changed only
  with all validators restarted together, to a height above the chain tip, and only after
  the wallets and relayers that build these payloads sign the chain id.
- `DelegatorHoldWeightActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`). From the height the staker
  pool of an epoch (`splitStakeShare`) weighs each indexed delegator by its locked ZNHB
  less the ZNHB its own pending POS authorizations hold (`pos.Lifecycle.PendingHeld`),
  and a delegator whose whole locked balance is held counts for nothing. A pending hold
  moves ZNHB into the payer's locked balance and that balance was the whole weight, so a
  wallet that delegated a little and then locked a large amount in a refundable hold took
  a share of the emission far above its stake (ledger PL-EXT-25). Which hold counts is read
  from the state at settlement, so it is the same on every node; the epoch settled in the
  first block at or after the height is the first to use the new weight. Before the height
  the weight is the whole locked balance, as before. The locked balance is written by
  only two things, a delegation (`StakeDelegate` and the reward engine's settlement of
  it) and a POS hold, so the locked balance less the holds is what the wallet delegated.
  A wallet is delegated to one validator at a time (a second delegation to another
  validator is refused until the first is fully undelegated), so it is indexed under at
  most one. Changed only with all validators restarted together, to a height above the
  chain tip.
- `PotsoAbuseGuardsActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`), and so must the five guard values
  it governs: `MinStakeToWinWei` and `MinEngagementToWin` (`[potso.weights]`),
  `MinStakeToEarnWei`, `QuadraticTxDampenAfter` and `MaxUserShareBps` (`[potso.abuse]`).
  From the height `processPotsoRewardEpoch` applies them as configured; before it (and
  with the height 0 = never) it applies them as 0 whatever the node's file says
  (`withoutPotsoAbuseGuards`, and `MaxUserShareBps` of the reward configuration), so the
  values can be written into the files ahead of the height and every epoch closed before
  it is computed as it was. The guards stop an account with no stake that makes two
  one-wei transfers an epoch (a transfer costs a percentage of its value, so that costs
  nothing) from reaching the engagement cap and taking up to 30 percent of the epoch's
  emission and of the governance weight (ledger PL-EXT-39, PL-PO-01). The epoch settled in
  the first block at or after the height is the first to use them, the same block that
  feeds the governance weight snapshot, so a proposal's voting power follows the same
  values. The values themselves are the owner's choice; the live files hold 0 for all
  five. Changed only with all validators restarted together, to a height above the chain
  tip.
- `HeartbeatTimestampBoundActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`). From the height `applyHeartbeat`
  refuses a heartbeat transaction whose payload timestamp is more than
  `heartbeatMaxAheadSeconds` (120) after the block's own time, before it changes anything,
  with `ErrHeartbeatTooSoon` (so the mempool prunes it as it prunes a replayed heartbeat).
  A timestamp of 0 still means the block time and a timestamp in the past is still
  accepted. Before the height (and with the height 0 = never) any signed timestamp is
  accepted, as on both live validators. The reason: `rolloverEngagement` walks one day at a
  time from the account's stored day to the heartbeat's day and `emitScoreUpdates` writes
  one event per day, so a timestamp dated year 9999 made every validator walk about 2.9
  million days (ledger PL-EXT-71). A heartbeat already on the chain is replayed as it was.
  Changed only with all validators restarted together, to a height above the chain tip.
- `LendingClosedDayClampActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`). From the height
  `scheduleNextLendingAutoDebit` and `scheduleNextLendingDepositPayout` put the next cycle
  of a fixed-term loan's auto-debit or a fixed-term deposit's payout on
  `max(its due day, the day of the scheduling block)` (`lendingDueDay`); before it (and
  with the height 0 = never) on its due day, as on both live validators. The settlement
  scans the days after its watermark up to today and then sets the watermark to
  yesterday, so an entry on a day at or below the watermark is never scanned again: a cycle
  that succeeds late schedules the next one by its absolute due time, which for the last
  two cycles of a tenure of 30k+1 days is one day after the previous, and the final
  installment of a loan was never collected and the final payout of a deposit, principal
  included, never made (ledger PL-EXT-83). An entry put on today is scanned by the next
  block's pass. Cycles already stranded on a closed day before the height stay stranded:
  the rule does not go back. Changed only with all validators restarted together, to a
  height above the chain tip.
- `PotsoTreasuryShortfallActivationHeight` must be identical on every validator
  (`config/config.go`; carried as `core.ValidatorRules`). From the height
  `processPotsoRewardEpoch` takes the reward budget to be the emission instead of capping
  it at the treasury balance first (the cap made the shortfall check unreachable); when the
  treasury cannot cover what the epoch would pay the shortfall is detected before anything
  is written and the epoch is recorded as a zero payout (no winners, no claims, no debit;
  `RewardEpochMeta.Shortfall`, a `potso.reward.shortfall` event, an error log). It is not an
  error: this runs from the block lifecycle on every block, so an error would abort every
  block and be retried for ever. The epoch is marked processed. Before the height an
  underfunded treasury pays a capped epoch, as before. The claim-mode half of PL-DC-29 is
  not changed: `potso_reward_claim` is disabled and there is no claim transaction, so there
  is nothing to reserve for. It changes what an underfunded epoch pays, so it is changed only
  with all validators restarted together, to a height above the chain tip.
- The consensus digest (`config.Config.ConsensusDigest`, `docs/consensus/activation-policy.md`)
  is the check that the settings above, the governance, POTSO, swap, lending,
  subscription and global settings and `NHB_MASTER_TREASURY` are the same on both
  validators. Every node logs its root at start-up, leaves it in
  `consensus_config_digest.txt` and sends it in the p2p handshake (advisory, log only);
  `nhb-snapshot check-parity` compares two files. A setting added to `config.Config`
  must be declared digested or node-local (`config/digest.go`), and a rule pinned in
  `cmd/nhb-snapshot/pins.go` must move the digest: tests fail otherwise.
- `LendingAccountingFixesActivationHeight` must be identical on every validator
  (`config/config.go`). From the height the lending engine applies its corrected
  accounting rules (`Engine.SetAccountingFixes`, set by `StateProcessor.lendingEngine`
  from the block height): a fixed-term borrow accrues the market first (PL-R5T-50),
  and a borrow's developer fee, paid to its recipient at once, is not also added to the
  withdrawable developer-fee counter (PL-R5T-52), and a lending position still recorded
  on a plain account is taken by the default pool only and cleared from the plain
  account when taken (PL-EXT-16; non-default pools also do no per-pool sweep of every
  account).
  It changes the state a lending transaction leaves behind, so it is changed only with
  all validators restarted together, to a height above the chain tip. Off (0) it is
  byte-identical to the earlier release.
- `LendingPoolLimitsActivationHeight`, `LendingMaxPools` and `LendingPoolIDMaxLength`
  must be identical on every validator (`config/config.go`). From the height a pool
  other than `default` exists only if `TxTypeLendingCreatePool` created it, a pool id
  uses letters, digits, `.`, `_` and `-` only and is at most `LendingPoolIDMaxLength`
  long (0 = unlimited), at most `LendingMaxPools` pools exist (0 = unlimited), and
  creating a pool is refused while the lending module is paused (PL-R5T-13). The
  refusals are `ErrLendingPoolIDInvalid` and `ErrLendingPoolLimitReached` (pruned) and
  `ErrLendingPoolNotFound` (skipped). A rule that decides which transactions are valid:
  changed only with all validators restarted together, to a height above the chain tip.
- `LendingRefPriceGuardsActivationHeight`, `LendingRefPriceMaxAheadSeconds` and
  `LendingRefPriceMaxAgeSeconds` must be identical on every validator
  (`config/config.go`). From the height `applyLendingRefPriceTransaction` refuses a
  bundle whose timestamp does not fit a signed 64-bit number or whose median is zero
  (`ErrLendingRefPriceTimestampRange`, `ErrLendingRefPriceRateTooSmall`: pruned), one
  dated more than `LendingRefPriceMaxAgeSeconds` before the block time
  (`ErrLendingRefPriceTooOld`: pruned; 0 = no such check) and one dated more than
  `LendingRefPriceMaxAheadSeconds` after it (`ErrLendingRefPriceTooFarAhead`: skipped,
  it becomes valid when the block time catches up; 0 = no such check) (PL-EXT-13,
  PL-R5T-53). A rule that decides which transactions are valid: changed only with all
  validators restarted together, to a height above the chain tip.
- `LendingRiskActivationHeight` and the limits of `[lending.risk]` and `[lending.breaker]`
  (`LiquidationBonusBps`, `BorrowCapPerBlockWei`, `BorrowCapUtilisationBps`, `PauseSupply`,
  `PauseBorrow`, `PauseRepay`, `PauseLiquidate`, `CircuitBreakerActive`, `MaxTotalSupplyWei`,
  `MaxTotalBorrowWei`, `MaxTotalCollateralWei`) must be identical on every validator
  (`native/lending/config.go`, read by `Config.RiskParameters` in both entry points). Before
  the height `StateProcessor.lendingEngine` clears them (`RiskParameters.WithoutGatedLimits`),
  as every earlier release did; from it the engine enforces them in Borrow, BorrowFixedTerm,
  Supply, SupplyFixedTerm, DepositCollateral, Repay and Liquidate (PL-DC-19). A rule that
  decides which transactions are valid and how much a liquidator is paid: changed only with
  all validators restarted together, to a height above the chain tip. `lend_getPools` shows
  the limits in force for the next block (`Node.LendingRiskParameters`). The `lending.*` keys in the
  governance parameter allow-list (`config/config.go`) have no reader: a `param.update` proposal for them
  is accepted and changes nothing, so these limits are changed in the file, on every validator, never by
  proposal (recorded under PL-DC-19; wiring them to governance is a separate decision).
- `LendingFeeWithdrawActivationHeight` must be identical on every validator
  (`config/config.go`). From the height `TxTypeLendingWithdrawProtocolFees` (0x4D) is valid:
  an ordinary signed transaction from an account holding `ROLE_LENDING_PROTOCOL_ADMIN`
  (granted by a `role.allowlist` proposal once the name is in `[governance] AllowedRoles`)
  pays up to the pool's protocol-fee counter, and no more than the pool's own free liquidity (what
  lenders supplied plus fixed-term deposit principal, less what is lent out), to the recipient named in the payload,
  and lowers the pool's supplied total by the same amount. Replay is the sender's nonce. Before
  the height the byte is rejected as `ErrUnknownTransactionType`, as on every earlier release.
  It is a new transaction type, so it needs every validator restarted together on the same
  binary before the height; a node on an older binary would reject the block (PL-DC-19). This is
  deliberately not the senderless, embedded-signature design of the public `main` branch, so the
  intent-registry namespace issue (PL-R5T-48) does not arise on this line.
- `PaymasterSponsorBindingActivationHeight` must be identical on every validator
  (`config/config.go`). From the height a sponsor's signature (`PaymasterR/S/V`) must
  be over `types.Transaction.PaymasterSponsorDigest(sender)` (a domain tag, the
  transaction digest and the sender), checked in `StateProcessor.EvaluateSponsorship`;
  the older signature over the transaction digest alone is refused. That closes the
  reuse of one sponsorship by many senders and the use of a sponsor's signature as an
  envelope signature. `Hash()`, the sender's signature and transaction ids are
  unchanged. It decides which sponsored transactions are valid, so it is changed only
  with all validators restarted together, to a height above the chain tip, after the
  sponsors' signers sign the new digest (`Transaction.SignPaymasterBound`). A
  sponsor signature made under the older rule and already broadcast stays a valid
  envelope signature, so it must not be reused after the height.
- `LoyaltyPaymasterConsentActivationHeight` must be identical on every validator
  (`config/config.go`). From the height `Engine.ApplyProgramReward` debits a business's
  paymaster only if it is the business owner's own wallet or the standing record
  `loyalty/paymaster-assigned/<business><wallet>` exists; any other reward is skipped
  with reason `paymaster_not_consented`. `Registry.SetPaymaster` writes that record
  when a wallet other than the owner is assigned (and clears the previous wallet's),
  re-affirming a paymaster that has no record needs the wallet's consent again, the
  current paymaster may withdraw itself, and a registered merchant address may remove
  itself (`applyLoyaltyRemoveMerchant`). It changes which rewards are paid and which
  loyalty transactions succeed, so it is changed only with all validators restarted
  together, to a height above the chain tip, after the stored assignments of live
  businesses have been read and any that name a wallet that never consented cleared.
- `LoyaltyCounterpartyCapActivationHeight` and `LoyaltyCounterpartyCapZNHBWei` must be
  identical on every validator (`config/config.go`). From the height the loyalty
  base-reward path (`loyaltyRewardState.LoyaltyGlobalConfig`) uses the configured
  per-pair daily cap when the stored loyalty configuration has `DailyCapCounterparty`
  of zero (the relaunch genesis stores none); a stored cap is left as it is, and the
  configuration the RPC reads is unchanged. It limits the reward one pair of accounts
  earns per UTC day; it does not limit NHB passed along a chain of fresh accounts. It
  changes how much of each base reward is paid, so it is changed only with all
  validators restarted together, to a height above the chain tip.
- `BuybackMinAskActivationHeight` and `BuybackMinAskZNHBWei` must be identical on every
  validator (`config/config.go`). From the height `applyBuybackAsk` refuses an ask below
  the amount (`ErrBuybackAskTooSmall`, pruned like the fixed minimum of one ZNHB) when it
  is larger than one ZNHB; the limits of 1,024 pending asks per epoch and 8 per seller
  are unchanged. It decides which ask transactions are valid, so it is changed only with
  all validators restarted together, to a height above the chain tip.
- `SubscriptionsPauseSettlementActivationHeight` must be identical on every validator
  (`config/config.go`). From the height `settleSubscriptionCharges` returns before it
  reads the watermark while the subscriptions module is paused, so nothing is charged,
  no failed attempt is recorded and the days skipped are settled again on unpause (one
  catch-up charge per due subscription). It decides what a block's settlement leaves
  behind, so it is changed only with all validators restarted together, to a height
  above the chain tip. No shipped transaction can set the pause on a running chain, so
  it matters only if genesis or hand-edited state carries it.
- `SubscriptionsChargeHistoryMigrationActivationHeight` must be identical on every
  validator (`config/config.go`). From the height the settlement's registry reads a
  subscription's charge history from the old list key (`subscriptions/charges/<id>`)
  when the subscription has no charge count, and the first attempt recorded copies that
  list into one key per attempt, sets the count and continues the numbering (the old
  list is left in place and never read again). It changes how an attempt is numbered and
  which keys it writes only for a subscription that holds history under the old key; on a
  chain that never wrote it, nothing differs. It is changed only with all validators
  restarted together, to a height above the chain tip.
- `SponsoredTransferFeeCreditActivationHeight` must be identical on every validator (`config/config.go`). From the height the fee debited from a sponsor for a sponsored NHB transfer is credited to the transfer fee collector whenever it is positive, not only while the transfer gas policy is enabled (the policy is disabled whenever its free-tier spend limit is zero); below the height, with the policy disabled, the debited fee is destroyed. It decides which transactions are valid or what a block leaves behind, so it is changed only with all validators restarted together, to a height above the chain tip.
- `SwapVoucherSignedBudgetActivationHeight` must be identical on every validator (`config/config.go`). From the height the USD budget the treasury's curve-price guard compares a voucher mint with is the signed fiat amount of a USD voucher, a submission whose usdAmount is set and differs from it is refused (a payload error, pruned) and a voucher in any other currency is refused (unsupported fiat, pruned) because nothing the issuer signs gives its value in USD; below the height the budget is the submission's own usdAmount, which is not signed. It decides which transactions are valid or what a block leaves behind, so it is changed only with all validators restarted together, to a height above the chain tip.
- `BuybackUnpriceableEpochRefundActivationHeight` must be identical on every validator (`config/config.go`). From the height an epoch whose buyback price cannot be computed (a governed discount or safety margin of 10,000 basis points makes the highest price zero) is settled like an epoch with no reference price: nothing is bought, every seller gets the escrowed ZNHB back, the epoch's reference price and asks are cleared and the settlement event reports that no reference price was used; below the height the settlement fails with "price must be positive" and the block that closes the epoch is rejected by every validator. It decides which transactions are valid or what a block leaves behind, so it is changed only with all validators restarted together, to a height above the chain tip.
- `BridgeActivationHeight`, `MaxLockZNHBWei`, `Signers`, `SignerThreshold`, `Guardian`, `MaxReleaseZNHBWei`, `WindowBlocks`, `WindowCapZNHBWei`, `DelayAboveZNHBWei`, `DelayBlocks` and `Rotations` (`[bridge]`) must be identical on every validator (`config/bridge_amm.go`, parsed by `core.BridgeAmmSettingsFromConfig`; the node refuses to start with an activated bridge and any missing or inconsistent value).
  From the height `TxTypeBridgeLock` (0x4E) and `TxTypeBridgeRelease` (0x4F) are valid; before it, and while the height is 0 (the shipped value), the two bytes are rejected as
  `ErrUnknownTransactionType`, as on every earlier release. A lock moves ZNHB from the sender into a module-owned escrow account (`bridgeEscrowAddress`) and records the destination chain and
  address. Stage 1 of the trust plan (ledger PL-BC-C): no single key releases ZNHB. One transaction type, 0x4F, carries five operations (`Op` in the payload): 0 release, 1 execute, 2 cancel, 3 pause, 4 unpause.
  A release (any sender) carries the amount in its payload and at least `SignerThreshold` distinct signatures of the signers in force over `BridgeReleaseDigest` (domain `NHB_BRIDGE_RELEASE_V1`, the network chain id,
  the escrow address, the burn id, the recipient, the amount; keccak256; 65-byte signatures with v 0/1 or 27/28); a bundle may hold at most one signature per signer, and one signature that is malformed or not from a
  signer in force fails the whole release. It pays the escrow's ZNHB to a recipient once per burn id and never more than what was locked, not yet released and not already waiting. One release is at most
  `MaxReleaseZNHBWei`; the releases accepted (paid or queued) in one window of `WindowBlocks` blocks (`height / WindowBlocks`) are at most `WindowCapZNHBWei`, counted when a release is accepted and not given back
  by a cancel. A release above `DelayAboveZNHBWei` is queued instead of paid and can be executed by anyone from `DelayBlocks` blocks later (the block at the due height itself is allowed); the ZNHB stays in the
  escrow meanwhile and the queued amount is part of `bridgeTotals.Pending`. The guardian pauses (locks, releases and executions stop), unpauses and cancels a waiting release; it cannot release, it must not be a
  signer, and a cancelled burn id stays spent (the ZNHB stays in the escrow; paying the holder needs a new signed burn id). `Rotations` replace signers, guardian and limits from a scheduled height (nothing
  inherited); a queued release keeps its due height across a rotation. Neither a lock nor a release creates or destroys ZNHB, neither touches the Sale or Reward pool counters, and the admin wallet can neither lock nor receive, so
  `CheckZNHBSupplyInvariant` is unaffected; `CheckBridgeInvariant` (escrow balance at least locked minus released, and what is waiting at most what is outstanding) is checked by the tests after every operation. New
  transaction types, so every validator is restarted together on the same binary before the height. `bridge_getStatus` is a read-only view of the escrow: escrow, outstanding, pending, `escrowCovered`,
  the signers, threshold, guardian, caps, the window's use and the next rotation. `bridge_getLock` (one lock by id), `bridge_listLocks` (locks above an id, at most 200 per call, with the newest id) and
  `bridge_getRelease` (a burn id: paid, pending with its due height, cancelled, or none) are read-only too; they add no setting, so the settings digest and every state root are unchanged.
- `AmmActivationHeight`, `SwapStartHeight`, `FeeBps`, `MaxSwapBps`, `LiquidityProvider`, `MinReserveZNHBWei` and `MinReserveNHBWei` (`[amm]`) must be identical on every validator.
  From `AmmActivationHeight` `TxTypeAmmAddLiquidity` (0x50) and `TxTypeAmmRemoveLiquidity` (0x51) are valid for the configured provider only, and `TxTypeAmmSwap` (0x52) from `SwapStartHeight`
  (0 = swaps never open); before that the bytes are unknown types. One constant-product pool (`native/amm`, exact integer arithmetic, outputs rounded down, so the reserve product never falls)
  holds its reserves in a module-owned account; one swap may add at most `MaxSwapBps` of the reserve it adds to, no swap or partial removal takes a reserve under its floor, the fee stays
  in the pool, and the admin wallet and the pool's own account cannot swap. Nothing that prices anything (the curve, the buyback reference price, lending valuation) reads the pool: a test
  scans the sources for that. `CheckAmmInvariant` (the pool account holds at least the recorded reserves) is checked by the tests after every operation. `amm_getPool`, `amm_quote` and
  `amm_getTrades` are read-only views.
  From the same `AmmActivationHeight` every peer-to-peer market fill is also recorded in a market-wide fill sequence (`market/fillSeq`, `market/fillAt/<n>`, written by
  `marketStateAdapter.AppendFill` after the per-buyer and per-seller indexes, which are unchanged); there is no separate setting. One small key per fill and one counter, never a growing list
  under one key (`Manager.KVAppend` rewrites the whole list each time). Each fill is also folded into a per-UTC-day row (`market/fillDay/<day>`, `market/fillDayLast`; `state/market_day.go`: first,
  highest, lowest and last price as exact NHB/ZNHB pairs, the volume in both assets, the number of fills), so state grows by one small row per day for the reads. The day is the fill's own
  timestamp, which the market engine takes from the block header, never from a clock. It adds state keys and nothing else: no balance, no fill
  amount, no fee and no listing changes, and below the height, and while `AmmActivationHeight` is 0, no key is written and the state is byte-identical to the earlier release. Fills made before the
  height are not in the sequence or the day rows. `amm_getTicker`, `market_getTicker`, `market_getRecentFills` and `market_getDailyStats` are read-only views over the pool, the open listings, that sequence and the day rows; the prices they show are
  information only and nothing that prices anything reads them (the source-scanning test covers the new files). A node that has the code but not the setting is therefore safe to run next to nodes
  that do not have the code, until the height; from the height every validator must have it.
- `UniformTransferFeeActivationHeight` must be identical on every validator (`config/config.go`; carried as `core.StateProcessor.uniformFeeFromHeight`, by `StateProcessor.Copy` and the node's rebuild path). From the height a transfer (NHB or ZNHB) that names a configured fee domain in `merchantAddr` pays exactly the ordinary transfer fee and nothing more: the same free allowance and rate as a transfer naming no domain, with no merchant rate (`MDRBasisPoints`) and no per-domain monthly free tier. The fee the sender is debited is credited to the domain's route wallet for the asset when it has one (through the same buyback split and aliasing rules as a domain fee, `creditDomainFee`) and to the transfer fee collector otherwise; a fee a sponsor pays goes to the collector as before. The domain's fee totals and the `fees.applied` event are still written; the domain's monthly counters are not advanced; a domain fee payer of `recipient` is not honoured. A transfer that names no domain, or one the node has no policy for, is untouched at every height. Below the height, and with the setting at zero, the older behaviour is byte-identical (the ordinary fee and the merchant fee are both charged). The full history of the live chain to height 2,344,655 contains no transaction that names a fee domain. It decides the balances a block leaves behind, so it is changed only with all validators restarted together, to a height above the chain tip.
- `TransferFeeGovernanceActivationHeight` and `TransferFeeGovernanceDelayBlocks` must be identical on every validator (`config/config.go`; carried as `core.StateProcessor.feeGovFromHeight` and `feeGovDelayBlocks`, by `StateProcessor.Copy` and the node's rebuild path). From the height the proposal kind `policy.feeParams` is accepted (`governance.Engine.SetFeeSchedule`, handed the block height by `StateProcessor.governanceEngine`; below the height the kind is refused as an unknown one at submission and at execution), and the transfer fee schedule is read from chain state instead of only from the file: `StateProcessor.effectiveTransferGasPolicy` lays the schedule that governs the block's height (parameter key `fees.transferSchedule`, `governance.FeeScheduleRecord.InForce`) over the configured transfer policy, for both assets, in the NHB and ZNHB transfer paths and in the free-allowance check. The schedule is a rate for NHB and one for ZNHB (each 0 to 100 basis points) and a free allowance per wallet and asset (at most 10^27 wei; zero only with an explicit flag). A passed proposal, executed in block H, is stored as a pending schedule that governs from block H plus the delay (the configured delay raised to `governance.MinFeeScheduleDelayBlocks`, 100): the height is in the record, so every validator and follower applies it in the same block. A schedule that has taken effect is moved to the record's active slot by the next execution, and a schedule still waiting is replaced by a later one. A stored record that does not parse or whose values break the bounds is ignored (the configured values apply). The collector, the allowance window and the monthly free tier are not governed. With the setting at zero no state is read for the schedule and the kind does not exist. It decides what every transfer is charged, so it is changed only with all validators restarted together, to a height above the chain tip.
- `TransferFeeStrictAllowanceActivationHeight` must be identical on every validator (`config/config.go`; carried as `core.StateProcessor.strictAllowanceFromHeight`, by `StateProcessor.Copy` and the node's rebuild path). Until the height a transfer is free in full while the sender's recorded spend in the asset is below the free allowance (`TransferFreeTierSpendWei`, or the governed allowance), measured before the transfer, so the transfer that crosses the line is free in full. From the height the allowance is strict (`TransferGasPolicy.FeeWithAllowance`, exact `big.Int`, no float, no clock): the free part of a transfer is `min(max(0, limit - spent), amount)`, the chargeable part is the rest, and the fee is the asset's rate on the chargeable part, rounded down (a chargeable part so small that the fee rounds to zero pays nothing). It applies to the ordinary fee of both assets, so to a transfer that names a fee domain under `UniformTransferFeeActivationHeight`, and to the allowance of a governed schedule. It does not change what is recorded as spent (the whole amount), the allowance window, the fee collector, a sponsored transfer (the sponsor's fee is on the whole amount and the sender's allowance is not consulted, as before; the transfer still counts as the sender's spend) or the monthly free tier (`TransferMonthlyFreeTierActivationHeight`, which where it applies still waives the whole remaining fee). With the policy disabled (no allowance) the whole amount is charged under either rule. `fees_getTransferQuote`, `fees_getTransferStatus` and `fees_getTransferSchedule` report the rule for the next block, from the same function. Below the height, and with the setting at zero, the block path is byte-identical to the earlier release. Of the 403 transfers in the stored real history (heights 1 to 2,344,655), 5 cross the line, all below height 2,321,017. It decides what a transfer is charged, so it is changed only with all validators restarted together, to a height above the chain tip.
- The `[global.Paymaster.AutoTopUp]` policy (`config/global.go`) must be identical on every
  validator while `Enabled` is true: whether a top-up runs, and so which balances a
  block leaves behind, depends on it. It is off in the shipped config. When enabled it
  needs `SponsorRole` (a sponsor without the role is refused, `sponsor_not_registered`)
  and `AggregateDailyCapWei` (a cap over all sponsors per UTC day, fee included,
  `global_daily_cap_exceeded`); config parsing refuses to enable it without both,
  because the per-sponsor `DailyCapWei` and cooldown are keyed by the sponsor address
  and a new address would otherwise get a new allowance. The aggregate total is the
  day record whose sponsor key is `aggregate`, written only when the cap is set.
- `BuybackPriceGuardActivationHeight` must be identical on every validator
  (`config/config.go`). From the height governance refuses a buyback discount or
  safety margin of 10000 basis points, the values that make the highest buyback price
  exactly zero: a proposal of the buyback parameters kind that names one is refused
  when it is submitted, and executing a proposal that names one is refused at the one
  function every governance write of those two parameters goes through
  (`Engine.paramStoreSet`), leaving the stored parameters as they were. A proposal
  stored before the height is not examined again; only its execution is refused. It
  decides which transaction is valid, so it is changed only with all validators
  restarted together, to a height above the chain tip, and
  `EpochSettlementFailsafeActivationHeight` is set to the same height or lower so a
  value stored earlier is already handled by the settlement.
- `EpochSettlementFailsafeActivationHeight` must be identical on every validator
  (`config/config.go`). From the height, in the block that closes an epoch: (1) a
  buyback price that is zero or cannot be computed means no purchase this epoch, every
  ask is refunded in full, and the events `buyback.epoch.settled` (as when no
  reference price was filed) and `buyback.epoch.refunded` (with the reason) are
  emitted instead of the block failing; (2) a reward payout larger than the admin
  wallet's liquid ZNHB is scaled down to that balance, the way it already is to the
  reward pool, the epoch is recorded as short (`rewards.epoch_closed` carries
  `short_reason`), and the block does not fail. It decides what the epoch-closing
  block leaves behind, so it is changed only with all validators restarted together,
  to a height above the chain tip.
- `SaleReferenceActivationHeight`, `SaleReferenceMaxAgeSeconds` and `SaleReferenceMaxStepBps`
  must be identical on every validator (`config/config.go`; carried in `core.ValidatorRules`,
  logic in `core/sale_reference.go`, state in `core/state/sale_reference.go`). From the height the
  price of every sale from the treasury Sale Pool (the direct purchase `applyBuyZNHB`, the swap
  voucher mint's curve guard and `QuoteBuyZNHB`) is the higher of the Genesis curve's cost and the
  latest recorded outside reference price times the amount, so the curve is the floor and the
  market lifts it. The reference is the signed number the treasury buyback already takes
  (`TxTypeBuybackRefPrice`: an exact fraction NHB per whole ZNHB with an M-of-N signature bundle
  from the genesis-declared signers); `applyBuybackRefPrice` also writes it to the one key
  `znhb/sale/ref` (rate, epoch, signed timestamp), overwritten by every accepted reference. It is
  NEVER read from the native pool or any peer-to-peer price (a constant-product pool's price
  scales with (1 + net buying / reserve) squared and is cheap to move; a test scans the source).
  While the rule is on: a missing reference, a zero `SaleReferenceMaxAgeSeconds` and a reference
  older than the maximum age (age = block time minus the SIGNED time) PAUSE sales
  (`ErrSaleReferenceUnavailable`, skippable); sales never fall back to the curve. A reference
  dated more than 300 seconds after the block is refused (`ErrSaleReferenceTooFarAhead`,
  skippable) so a signed time cannot keep it fresh beyond the maximum age; one already older than
  the maximum age at the block that records it is refused (`ErrSaleReferenceTooOld`, prunable);
  when `SaleReferenceMaxStepBps` is non-zero a reference that moves further than that many basis
  points, up or down, from a still-fresh previous one is refused (`ErrSaleReferenceStepTooLarge`,
  prunable), while a stale previous reference lets any new one in so a stuck feed can always be
  replaced. A refusal writes nothing (the check runs before the buyback record is written).
  Price only: no balance, supply counter, pool or fee changes shape, and below the height, and
  while the height is 0, the sale is priced by the curve alone and no key is written, so the state
  is byte-identical to the earlier release. It decides which sale is valid and at what price, so
  it is changed only with all validators restarted together, to a height above the chain tip, and
  it must be in force before `BridgeActivationHeight` is set (PL-BC-D). The reference-price
  signers' duty to attest the depth of the market they average is off chain: the runbook names the
  minimum depth. `znhb_getTokenomicsState` carries a `saleReference` object (active, paused, the
  rate, its signed time and age) only where the height is set; `znhb_quoteBuy` answers HTTP 503
  "sales are paused" instead of the curve while the rule is on and no reference is fresh.
- `POSHoldUnstakeGuardActivationHeight` must be identical on every validator
  (`config/config.go`, `POSHoldUnstakeGuardActivationHeight`): it decides whether an
  unstake that would take funds backing a pending POS hold is valid.
