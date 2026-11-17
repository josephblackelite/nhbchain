# POTSO Evidence and Penalties

Misbehaviour reports ("evidence") are recorded on-chain by a dedicated signed transaction type, and a penalty step runs during block processing. Code: `consensus/potso/evidence`, `consensus/potso/penalty`, `core/potso_evidence_tx.go`, `core/state/potso_evidence.go`, `state/bank/slash.go`, `rpc/modules/potso_evidence.go`.

## Submission path

Evidence is reported with a `TxTypeSubmitEvidence` (`0x4C`) transaction sent through `nhb_sendTransaction` like any other native transaction. There is no evidence-submission RPC method: `potso_submitEvidence` was removed and the node answers it as an unknown method (`rpc/http.go`, comment above the `potso_getEvidence` case). `nhb-cli` has no command for it.

- `Data` is the RLP encoding of the evidence as a list of seven items in this order: `Type` (string), `Offender` (20 bytes), `Heights` (list of uint64), `Details` (bytes), `Reporter` (20 bytes), `ReporterSig` (bytes), `Timestamp` (uint64) (`evidenceRLPShadow`, `consensus/potso/evidence/types.go`).
- The transaction must carry a normal envelope signature (`RequiresSignature` is true for this type, `core/types/transaction.go`). The sender is the recovered signer, its account nonce is checked before execution and consumed by a successful one (`validateSenderAccount`, `incrementNativeAccountNonce`).
- `Reporter` must be the sender, otherwise `ErrEvidenceReporterMismatch`. The reporter therefore signs twice: the transaction envelope, and `ReporterSig` over the evidence digest (below).
- The sender must have at least the minimum validator stake in its own `LockedZNHB` (`requireEvidenceReporterBond`), otherwise `ErrEvidenceReporterNotBonded`. The minimum is the governed `staking.minimumValidatorStake`, 10,000 ZNHB (`10000000000000000000000` wei) until a proposal changes it (`defaultMinimumValidatorStakeWei` in `native/governance/types.go`). `LockedZNHB` is what a validator's own stake transaction locks; ZNHB delegated in by others and POTSO stake locks ([stake](stake.md), which move ZNHB to a vault account) do not count.
- The POTSO module pause (`system/pauses`, module `potso`) blocks the transaction (`nativecommon.Guard` in `applySubmitEvidenceTransaction`).
- Mempool admission (`Node.validateTransaction`) runs the transaction against a copy of state while `Node.txSimulationEnabled` is true. It defaults to true and `SetTransactionSimulationEnabled` has no caller outside tests, so an invalid report is refused when it is submitted. A valid one is queued and gossiped; it is recorded in the state trie only when a block containing it is applied.

Order of checks in `applySubmitEvidenceTransaction`: pause, payload decode (`ErrEvidenceInvalidPayload`), reporter equals sender, `evidence.ValidateEvidence`, reporter bond, pruning of expired records, then the duplicate and quota checks below. On success the record is stored, `potso.evidence.accepted` is emitted and the sender nonce is incremented.

## Evidence payload

| Field | Type | Notes |
| --- | --- | --- |
| `Type` | string | Exactly one of `DOWNTIME`, `EQUIVOCATION`, `INVALID_BLOCK_PROPOSAL`. The transaction path does not change case (any other string fails as `invalid_type`); `evidence.ParseType`, which upper-cases, is used only for the `type` filter of `potso_listEvidence`. |
| `Offender` | 20 bytes | Account of the accused. Must not be the zero address. |
| `Heights` | list of uint64 | At least one, at most 64 (`MaxHeightsPerEvidence`). Ascending order (equal neighbours are allowed). |
| `Details` | bytes | At most 2,048 bytes (`MaxDetailsBytes`). Hashed. For `EQUIVOCATION` it must be an equivocation proof (below). |
| `Reporter` | 20 bytes | Must not be the zero address and must be the transaction sender. |
| `ReporterSig` | bytes | 65-byte secp256k1 signature. |
| `Timestamp` | uint64 | Included in the signing digest. A negative value cannot be encoded (`Evidence.EncodeRLP`) and is refused as `invalid_timestamp`. |

### Canonical hash and signature

```
hash   = BLAKE3-256( uint32be(len(TYPE)) || TYPE || offender(20 bytes)
                     || uint32be(len(heights)) || heights (each uint64 big-endian, sorted ascending)
                     || uint32be(len(details)) || details )
digest = SHA-256( "potso_evidence|" || hex(hash) || "|" || decimal(timestamp) )
```

`hex(hash)` has no `0x` prefix. The reporter signs `digest`; the recovered address must equal `Reporter` (`CanonicalHash`, `SigningDigest`, `ValidateEvidence`).

### Equivocation proof

For `EQUIVOCATION`, `Details` must be JSON `{"height": H, "round": R, "voteType": 1|2, "voteA": {"blockHash": "hex", "signature": "hex"}, "voteB": {...}}` (`1` prevote, `2` precommit). Each vote's signature must be a 65-byte signature over `SHA-256(JSON({"blockHash", "round", "type", "height"}))` (the BFT vote payload) that recovers to the offender's address, and the two block hashes must differ (`VerifyEquivocationProof`). In addition `ValidateEvidence` requires the proof's `height` to be one of the report's `Heights` and not above the current block height, so the age check on the listed heights applies to the double-sign itself. `DOWNTIME` and `INVALID_BLOCK_PROPOSAL` have no proof requirement beyond the reporter signature.

### Offense identity

A report is recorded, and penalised, once per offense, not once per hash (`Record.Offense`). For `EQUIVOCATION` the offense is the offender plus the proof's `height`, `round` and `voteType`: `BLAKE3-256("potso_equivocation_offense" || offender || height(8 bytes) || round(8 bytes) || voteType(1 byte))`. Writing the same double-sign another way (votes swapped, other JSON layout, other heights listed) gives another report hash but the same offense. For the other two types the offense is the report hash. An offense is anchored at the proof height (`EQUIVOCATION`) or the smallest listed height.

## Validation

`ValidateEvidence` runs in the transaction path with the height of the block being applied and `DefaultMaxAgeBlocks = 8640`. It rejects, with these reasons: `invalid_type`, `invalid_offender`, `invalid_reporter`, `empty_heights`, `oversized` (more than 64 heights or more than 2,048 bytes of details), `invalid_timestamp`, `unsorted_heights`, `future_height` (a height greater than the current block height), `expired` (a height more than 8640 blocks old), `invalid_signature` (length, recovery or reporter mismatch) and `invalid_equivocation_proof` (including a proof height that is not listed or is above the current height). `unknown_height` is defined but is not checked in this path, because no height lookup is supplied. The 8640-block window is a block count: its duration follows the block interval ([block cadence](../consensus/block-cadence.md)).

Further refusals in `applySubmitEvidenceTransaction`:

| Error | Meaning |
| --- | --- |
| `ErrEvidenceAlreadyRecorded` | The same report hash, or another report of the same offense, is already in state. |
| `evidence.ErrReporterQuota` | The reporter already holds 4 live records (`MaxRecordsPerReporter`). Checked after the duplicate check. |
| `evidence.ErrIndexFull` | 256 records are live (`MaxPendingRecords`). Returned by `PotsoEvidencePutRecord`. |

Expired records are pruned before these checks, so a full index frees room in the block in which its records expire. When a block proposer meets a rejected evidence transaction (`classifyProposalError`, `core/node.go`): reasons decided by the report alone, `ErrEvidenceInvalidPayload`, `ErrEvidenceReporterMismatch` and `ErrEvidenceAlreadyRecorded` remove it from the mempool; `future_height`, `ErrEvidenceReporterNotBonded`, `ErrIndexFull` and `ErrReporterQuota` leave it there to be retried.

## Persistence and queries

- Record: `potso/evidence/record/<hash>` (`potsoEvidenceRecordKey`, written with `KVPut` on the plain key, so under a single `Keccak256(key)`).
- Index: `potso/evidence/index/v2`, a list of `{Hash, Offense, Offender, Reporter, AnchorHeight}` entries, oldest first, at most 256. It is deleted when empty.
- Penalty marker: `potso/penalty/applied/<offense 32 bytes><offender 20 bytes>`.

A record, its index entry and its penalty marker are deleted (`PotsoEvidencePrune`) once the anchor height is older than `currentHeight - 8640`. That is the same predicate `ValidateEvidence` uses to refuse a report as expired, so a pruned offense can no longer be reported and cannot be penalised twice.

RPC (no authentication; both handlers are read-only):

- `potso_getEvidence({ hash }) -> record`, where the record has `hash`, `type`, `offender`, `heights`, `details`, `reporter`, `reporterSig`, `timestamp`, `receivedAt` (block timestamp). Unknown hash, including a pruned one: HTTP 400, "evidence not found". The hash must be 32 bytes of hex.
- `potso_listEvidence({ offender?, type?, fromHeight?, toHeight?, page: { offset?, limit? } }) -> { records, nextOffset? }`. Newest first; `limit` defaults to 50 and is capped at 200. `fromHeight` / `toHeight` compare against the record's smallest height.

Event on acceptance (`core/events/potso_evidence.go`): `potso.evidence.accepted` with `hash`, `type`, `offender`, `height` (smallest height), `reporter`. The type `potso.evidence.rejected` is defined in the same file but nothing emits it.

## Penalty processing

`StateProcessor.processPendingEvidence` (`core/potso_evidence_tx.go`) runs from `Node.processPendingEvidenceForState` after `ProcessBlockLifecycle` when a block is built, validated or committed. Everything it reads and writes is in the block's own state. It prunes expired records, then for each index entry whose offense has no penalty marker it refreshes the offender's base weight from the account's current `Stake` (`Ledger.EnsureBaseline`, skipped when `Stake` is nil) and calls `penalty.Engine.Apply`. A report is therefore penalised in the block that records it (evidence transactions run first). The engine is built from `penalty.DefaultConfig()` with `SlashEnabled = true` and `EquivocationSlashBps = 10000`, and always passes `MissedEpochs = 0`.

Rules (`consensus/potso/penalty/rules.go`, amounts in wei):

| Type | Severity | Decay computed on the weight ledger | Slash |
| --- | --- | --- | --- |
| `EQUIVOCATION` | CRITICAL | `min(current, max(base * 5000 / 10000, 100))` | `base * 10000 / 10000`, capped as described below |
| `DOWNTIME` | MEDIUM | Ladder by missed epochs: 1 -> 200 bps, 2 -> 500 bps, 3+ -> 1000 bps of the current weight. With `MissedEpochs = 0` the decay is 0. | none |
| `INVALID_BLOCK_PROPOSAL` | HIGH | 300 bps of the current weight | none |

`penalty.Config` also has cooldown fields (7, 1 and 1 epochs); nothing in `Apply` reads them.

The weight ledger (`state/potso.Ledger`) is created empty for each call of `processPendingEvidence` and discarded afterwards. The decay is computed on it and reported in the event (`decayPct`, `newWeight`), but nothing stores it and nothing else reads it: POTSO reward weights are computed from stake locks and engagement ([weights](weights.md)). The lasting effects of a penalty are the `EQUIVOCATION` slash and the penalty marker. A `DOWNTIME` or `INVALID_BLOCK_PROPOSAL` report changes no balance.

The slash (`state/bank.ValidatorSlasher.SlashApplied`) forfeits `min(slash amount, Stake when Stake > 0, LockedZNHB)`. That amount is subtracted from the offender account's `LockedZNHB`, subtracted from `Stake` when `Stake > 0`, and added to the treasury's liquid `BalanceZNHB`. The treasury is the chain's admin wallet when genesis defines one, otherwise the address derived from `module/potso/slashed` (`evidenceSlashTreasury`). The event's `slashAmt` is the amount actually forfeited. When the treasury is the admin wallet the ZNHB pool ledger is adjusted by the same amount in the same step (`bookedSlasher`, `core/znhb_treasury_pool.go`).

The penalty marker is keyed by `(offense, offender)`. It is written by `Apply` and read before each application, so the same offense is never penalised twice, whichever report brought it to light.

### Event

`potso.penalty.applied`: `hash`, `type`, `offender`, `decayPct` (basis points rendered as a percentage with two decimals), `slashAmt`, `newWeight`, `block`, `idempotent`. The node appends this event only for non-idempotent applications, and an already-penalised offense is skipped before `Apply` is called, so `idempotent` is `false` in every event a block produces. `nhb_getSlashingEvents` returns these events from the node's event log (`core/event_log.go`): penalty events are kept in a pinned log of the last 100,000 fee, penalty and escrow events, and the log is held in memory only and is not part of chain state.

## Not implemented

There is no appeals or dispute mechanism: no RPC method, transaction type, event or evidence type for it. There is no `potso.audit` log stream in the code.
