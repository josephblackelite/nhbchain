# Consensus Invariants

This page lists the consensus rules that the code enforces today, with the
place each one is enforced. It is not a design wish list: a rule appears here
only if the code checks it. When changing consensus-critical code, keep these
checks intact.

## Block acceptance

Enforced in `core/node.go` (`commitBlock`, line 3828 onward, and
`ValidateBlock`):

1. **Contiguous heights.** A block is accepted only if
   `Header.Height == chain height + 1`. A block at or below the current height is
   accepted only if its header hash equals the stored block's header hash
   (idempotent replay); otherwise the error is `block height mismatch: got N
   want M`.
2. **Transaction root.** `ComputeTxRoot(transactions)` must equal
   `Header.TxRoot` (`tx root mismatch`).
3. **State root.** `ValidateBlock` re-executes the block on a copy of state and
   requires `Header.StateRoot` to equal the resulting pending root
   (`state root mismatch: header=... pending=...`, ~line 3810). Execution of a
   block's transactions, the end-of-block lifecycle (`ProcessBlockLifecycle`) and
   evidence processing all run inside that computation.
4. **Timestamp window.** `validateBlockTimestamp` (~line 322) requires the block
   timestamp to be no earlier than the previous block's timestamp. For a block
   the node produced or is voting on live (not the historical sync path) it must
   also be no later than `now + tolerance`, where the tolerance is
   `[governance] BlockTimestampToleranceSeconds` (5 in `config.toml`).
5. **Size limit.** `rejectOversizedBlock` runs before anything else in
   `commitBlock`.
6. **Quorum certificate on synced blocks.** For blocks that arrive by peer sync
   (not produced in the local BFT round) with height above
   `QuorumCertActivationHeight`, `QuorumCert.Verify`
   (`core/types/vote.go`, line 119) must pass against the validator set of the
   parent height (`validatorSetAtHeight(height-1)`; if that set cannot be
   resolved the block is rejected). Verification requires: the certificate's
   block hash equals the recomputed header hash; every signature is 65 bytes,
   recovers to the claimed validator, and that validator has positive power in
   the set; duplicates are counted once; and the signed power is at least
   `(2 * total + 2) / 3`. The check is off when the activation height is `0`
   or unset (`cmd/nhb/main.go`, line 153).

## Transaction execution

Enforced in `core/state_transition.go` (`executeTransaction`, line 2733):

1. **Chain ID.** `types.IsValidChainID(tx.ChainID)` must hold.
2. **Expiry.** For transaction types above `0`, `MaxBlockHeight` (if set) must
   not be exceeded at the current block height, and `IntentExpiry` (if set and
   there is no `IntentRef`) must not be before the block timestamp. Both use
   block values, not wall-clock time.
3. **Nonce.** For every type that has a sender, `tx.Nonce` must equal the
   account nonce exactly (`ErrNonceTooLow` / `ErrNonceTooHigh`,
   `validateSenderAccount`).
4. **Events are transactional.** When a transaction returns an error, the
   events it appended are discarded (lines 2851-2869; the transfer-paused and
   sponsorship rejections are the exceptions).
5. **Determinism inputs.** Values that end up in state come from the block, not
   the local clock. For example a heartbeat with no explicit timestamp uses the
   block timestamp (`applyHeartbeat`, `core/state_transition.go` line 6832).

## BFT engine

Enforced in `consensus/bft/bft.go`:

1. **Two-thirds quorum.** A commit needs precommits with power at least
   `(2 * totalVotingPower + 2) / 3`, computed over the validator set the engine
   holds for the height (`hasTwoThirdsPowerLocked`, line 1182).
2. **Validator set refresh.** The engine reloads `node.GetValidatorSet()` and
   recomputes total power when a new round starts (`startNewRound`) and after a
   commit (`commit`). A change made to the validator set while executing block
   `H` is therefore first used for height `H+1`.
3. **Height alignment.** The engine never proposes for a height at or below the
   node's committed height (see [BFT height sync](bft-height-sync.md)).
4. **Lock discipline.** A round timeout does not clear the proof-of-lock state;
   lock, valid block and polka history are reset only when the height changes
   (`startNewRound`, `resetLockStateLocked`), and the lock is persisted to
   `<DataDir>/polc_lock.json` and restored only for the same height.

## Validator set

Enforced in `core/state_transition.go` and `core/epochs.go`:

1. **Eligibility.** An address is in `EligibleValidators` only if
   `ValidatorRegistered`, it is not delegating its own stake to another
   validator, and its total `Stake` is at least
   `staking.minimumValidatorStake` (default 10,000 ZNHB). `setAccount`
   recomputes this every time the account is written.
2. **Selection timing.** The active set is recomputed only when
   `height % epoch length == 0` and height is not `0`
   (`ProcessBlockLifecycle`). The epoch length used is
   `epoch.DefaultConfig().Length` = 100; rotation is disabled, so all eligible
   accounts with a fresh heartbeat are included.
3. **Heartbeat freshness.** An account joins the set only if its last heartbeat
   is at most `max(5 x heartbeat interval, 15 minutes)` older than the boundary
   block time and not more than 2 minutes ahead of it
   (`validatorReadyForActivation`, line 558).
4. **Liveness fallback.** If no account qualifies, `fallbackValidatorSet` builds
   a set from the previous active set, eligible addresses and past epoch
   selections, still requiring registration, minimum stake, not delegated away,
   and at least one heartbeat ever (it skips the freshness window).
   `ensureValidatorSetLiveness` applies the same fallback at node start-up when
   the loaded set is empty (`core/node.go`, lines 398 and 479).
5. **Removal.** An account that stops satisfying rule 1 is removed from the
   active set in the same transaction (`setAccount`).

## ZNHB pool ledger

`CheckZNHBSupplyInvariant` (`core/state_transition.go`, line 1438) runs every
block from `ProcessBlockLifecycle`. When an admin/treasury wallet is configured
and the pools have been bootstrapped, it requires
`SalePool + RewardPool == admin BalanceZNHB + LockedZNHB + pending unbonds
(+ governance escrow)`. A violation is returned as a hard error. It checks the
internal consistency of the two pool sub-ledgers; it does not check any fixed
total supply.

## Other checks

- **Evidence.** `TxTypeSubmitEvidence` transactions are applied by
  `applySubmitEvidenceTransaction`; pending evidence is processed inside block
  validation (`processPendingEvidenceForState`, `core/node.go` line 4532).
  Evidence older than `evidence.DefaultMaxAgeBlocks` (8640 blocks,
  `consensus/potso/evidence/types.go`) is rejected by `ValidateEvidence`.
- **Sender quota.** Every signed transaction is subject to a per-sender
  requests-per-minute check at mempool admission, using the `[global.Quotas.Trade]`
  value (`applySenderQuotaLocked`, `core/node.go` line 1168, described in the
  `config.toml` comment on that section).

## Validation checklist for consensus changes

- Add tests next to the change (`core/*_test.go`, `consensus/bft/*_test.go`).
  The repository has determinism, fuzz and network bug-check targets in the
  `Makefile`: `bugcheck-determinism`, `bugcheck-fuzz`, `bugcheck-network`,
  `bugcheck-race`.
- `QuorumCertActivationHeight` must be identical on every validator
  (`config/config.go`, lines 114-127).
