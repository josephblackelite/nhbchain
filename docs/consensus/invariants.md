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
   holding strictly more than a third of the voting power, together with its own
   power a quorum, have been seen there; it is never in a round above
   `math.MaxInt32`, and it keeps future-round messages only within 8 rounds of
   its own (`round_sync.go`).
5. **Lock discipline.** A round timeout does not clear the proof-of-lock state;
   lock, valid block and polka history are reset only when the height changes
   (`startNewRound`, `resetLockStateLocked`), and the lock is persisted to
   `<DataDir>/polc_lock.json` and restored only for the same height.
6. **No conflicting vote.** The one function that signs a vote (`createVote`)
   refuses to sign a different value for a vote type in the (height, round) of the
   last vote it signed, and refuses an earlier round or height. The record is
   written to `<DataDir>/bft_sign_state.json` before the vote is signed
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

## Validation checklist for consensus changes

- Add tests next to the change (`core/*_test.go`, `consensus/bft/*_test.go`).
  The repository has determinism, fuzz and network bug-check targets in the
  `Makefile`: `bugcheck-determinism`, `bugcheck-fuzz`, `bugcheck-network`,
  `bugcheck-race`.
- `QuorumCertActivationHeight` must be identical on every validator
  (`config/config.go`, `QuorumCertActivationHeight`).
