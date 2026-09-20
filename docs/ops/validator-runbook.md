# Validator Operations Runbook

For onboarding a new validator see the
[validator onboarding guide](../validators/onboarding.md).

## Clock synchronization

Block timestamps are checked against the local clock and the previous block
(`Node.validateBlockTimestamp`, `core/node.go`):

- A block is rejected when its timestamp is later than `now + tolerance`, or
  earlier than the last accepted block's timestamp. The errors read
  `block timestamp outside allowed window: timestamp <ts> exceeds maximum <max> (now=<now> tolerance=<d>)`
  and `block timestamp outside allowed window: timestamp <ts> precedes minimum <min>`.
- The upper bound is skipped when historical blocks are being applied (sync).
- The tolerance is `BlockTimestampToleranceSeconds` in the `[governance]` section
  of `config.toml`. It defaults to 5 seconds, and `config.Load` forces 5 when
  `NetworkName` is `mainnet` or `nhbchain-1`. The value is also carried in the
  governance proposal policy.

Every validator needs a disciplined clock (for example chrony or ntpd) so its
`now` does not drift beyond the tolerance from its peers. When you see the
`block timestamp outside allowed window` error, compare the local clock with the
error's `now=` value and check the last accepted timestamp in state before
re-enabling signing.

## RPC hardening and transaction quotas

Settings are top-level keys in `config.toml` (`config/config.go`). Defaults come
from `config.Load`.

```toml
[RPCRouteRateLimits."nhb_sendTransaction"]
MaxTxPerWindow = 3
MaxTxPerIP = 3
```
- Align `RPCReadHeaderTimeout`, `RPCReadTimeout`, `RPCWriteTimeout`, and
  `RPCIdleTimeout` with upstream load-balancer/ingress settings to avoid idle
  disconnects. Document the final values in the deployment checklist.
- Set `[mempool] MaxTransactions` to a value that meets throughput expectations
  without exhausting memory. Nodes default to 4,000 pending transactions unless
  you opt into an unbounded queue by pairing `AllowUnlimited = true` with
  `MaxTransactions = 0`. Smaller devnet clusters can lower the ceiling in `config.toml`. The
  `NHB_MEMPOOL_MAX_TX` variable in `examples/.env.example` is not read by any
  code and has no effect on the node.
- Store TLS material in `RPCTLSCertFile` / `RPCTLSKeyFile` or enforce mutual TLS
  between the proxy and node. Rotate certificates on the same cadence as the
  proxy tier and track expirations in monitoring.

## Block production liveness

Block production is the whole chain: a validator that cannot build a block cannot
help commit one. The proposer therefore builds every proposal defensively, and the
defences are all local to the proposer -- they only choose which transactions it
includes, never how a block is validated, so validators running different versions
still agree on every block.

### What the proposer does

1. It applies the candidate transactions to a fresh copy of committed state and
   collects **every** failure instead of stopping at the first. Any failure discards
   that copy (a failed transaction may have half-applied) and the survivors run again
   from another fresh copy, up to `NHB_PROPOSAL_MAX_WAVES` (default 6) waves within
   a wall-clock budget (half the proposal timeout, at most one second).
2. Each failure gets a verdict. *Prune*: it can never apply (bad payload, spent
   nonce, expired) and is dropped from the mempool. *Skip*: it may apply later (a
   module pause, a rolling cap) and stays, backing off by height if it keeps being
   skipped. *Quarantine*: everything else. It is excluded, struck, and evicted after
   `NHB_POISON_STRIKES` (default 3) failures across separate blocks **and** a solo dry
   run showing it fails even alone against committed state, so a valid transaction that
   merely lost a same-block race is never evicted. A contained panic is quarantined at
   its first strike.
3. If waves or the budget run out, the clean prefix of the last wave is re-verified
   and used; failing that the proposer builds an **empty block**, which depends on no
   transaction and which validators already accept on every idle round.
4. If a build fails twice in a row, an asynchronous isolation pass applies each
   candidate alone and, if the set still fails in the epoch lifecycle, bisects out the
   transaction responsible.
5. If two of a validator's own proposals at one height fail to commit, it proposes an
   empty block for that height (`NHB_BFT_EMPTY_AFTER_FAILURES`, default 2, `0` disables).

What this cannot contain: a failure in the epoch lifecycle or evidence processing that
persists on the *empty* block. Nothing about a transaction causes it and no choice of
transactions fixes it; it is alarmed (below) and needs an operator.

### Transactions that read the wall clock

The proposer watches every transaction it applies for reads of the wall clock. A value
taken from the wall clock and written into state differs from one execution to the next,
so a block containing such a transaction could not be re-executed identically by another
validator. The proposer never proposes one (`NONDETERMINISM:` log line, metric
`nhb_mempool_tx_failures_total{disposition="nondeterministic"}`). No shipped transaction
type does this: escrow and trade timestamps (creation, update and the deadline checks)
and the legacy escrow migration come from the block time, as every other engine's do.
The detector is the guard against a change that brings such a read back, so a
`NONDETERMINISM:` line is a bug to report, not an operating condition.
`NHB_ALLOW_CLOCK_DEPENDENT_TXS=true` disables the detector and is for development
networks only.

### Log lines to grep

| Line | Meaning |
|---|---|
| `LIVENESS: block build failed; proposing empty block` | The full build failed; an empty block was proposed. Rate limited. |
| `LIVENESS: block build failed and empty-block fallback failed; this node cannot propose` | A node-level fault (state, lifecycle, evidence). Not fixable by excluding transactions. |
| `LIVENESS: no block committed for <N>s` | The committed height has not moved for the stall threshold (`NHB_LIVENESS_STALL_SECS`, default 60). Repeats every minute; `LIVENESS: recovered` follows once. |
| `LIVENESS: failed to propose block` | The consensus engine could not produce this round's proposal. |
| `LIVENESS: local validation of own proposal failed` | The block this node built failed this node's own validation. |
| `LIVENESS: panic in block build` | A panic escaped every inner guard and was contained at the top. |
| `NONDETERMINISM: transaction type ...` | A transaction that read the wall clock while applying was excluded. |
| `proposal containment` | One aggregated line per build that had any exclusion, with counts and the top error classes. |

Every `LIVENESS:` line carries structured fields (`height`, `since_commit_s`,
`consecutive_build_failures`, `last_build_error`, `mempool`, `inflight`,
`quarantined`, `bft_round`, `bft_locked_round`) that separate a peer that is down from
a build that is failing, from own proposals being rejected, from a lock that cannot be
satisfied.

### Metrics and alerts

Set `NHB_METRICS_ADDR` (for example `127.0.0.1:9101`) to serve `/metrics`; it is off
by default and unauthenticated, so bind it to loopback or a private interface. The
`consensus-liveness` group in `observability/alerts.yaml` alerts on
`nhb_consensus_seconds_since_last_commit`, `nhb_consensus_build_failures_consecutive`,
the empty-block fallback rate, wall-clock exclusions and recovered panics.

### Operator levers

| Variable | Default | Effect |
|---|---|---|
| `NHB_PROPOSAL_BUILD_BUDGET_MS` | `min(1000, proposal timeout / 2)` | Wall-clock budget for all waves of one build. |
| `NHB_PROPOSAL_MAX_WAVES` | 6 | Execution waves per build. |
| `NHB_POISON_STRIKES` | 3 | Failures before a transaction is eligible for confirmed eviction. |
| `NHB_POISON_SKIP_TTL` | 2h | Evict a transaction that has only ever been skipped once its first skip is older than this. A long governance pause can exceed it; affected transactions are simply resubmitted. |
| `NHB_INFLIGHT_LEASE_SECS` | 30 | How long an offered transaction stays hidden before it is offered again. |
| `NHB_LIVENESS_STALL_SECS` | 60 | Watchdog stall threshold. |
| `NHB_PROPOSER_EXCLUDE_TXTYPES` | empty | Comma-separated type bytes (`0x03,0x05`) this proposer will not include. They stay in the mempool with no strike. |
| `NHB_BFT_EMPTY_AFTER_FAILURES` | 2 | Own failed proposals at one height before an empty block is proposed. |

Malformed values are ignored with a warning; a typo never stops a validator.

### If both validators are stuck on a locked block

A validator that saw a prevote quorum for a block keeps that lock across rounds and
restarts (`polc_lock.json` in the data directory). If that block can no longer be
re-executed, neither validator can commit or replace it. Only after confirming that
**neither** validator committed a block at the stuck height (compare `GetHeight` on
both): stop both, delete `<DataDir>/polc_lock.json` on both, and start both together.
Deleting the lock on a validator that already committed the block, or restarting one
side alone, can fork the chain. Leave `bft_sign_state.json` (next section) where it is.

### The double-sign record

A validator that signs two different votes for the same height, round and vote type has
double-signed, and any two such votes are a complete slashing proof. So the engine
records every vote it signs, on disk, before it signs it (`bft_sign_state.json` in the
data directory), and refuses a vote that conflicts with the record. After a restart it
skips the rounds it already voted in, so a restart costs at most the round that was in
flight. Nothing else is asked of the operator, but:

- Keep `<DataDir>/bft_sign_state.json` with the validator's keys and chain data. A reset
  that wipes the data directory removes it with the chain, which is right.
- Never run one validator key on two hosts, and never copy the record between hosts: each
  process keeps its own record and cannot see the other's votes.
- Do not delete it while a height is in flight. It is not needed to clear a lock or to
  recover a stall, and a record for a height that has committed is ignored at the next
  start on its own.
- `SAFETY: refused to sign a conflicting vote` (event `double_sign_refused`) is the
  guard doing its job. Note the height and round; a validator that logs it repeatedly is
  receiving conflicting proposals or is being run twice.
- `SAFETY: could not persist the vote record` (event `sign_state_write_failed`) means the
  disk refused the write. The validator keeps voting and is protected while it runs, but a
  restart before the height commits is not: fix the disk first. A record that cannot be
  read at start is logged as `sign_state_read_failed` and ignored.
