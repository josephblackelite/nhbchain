# Activation policy for consensus changes

Why this exists: the node cannot refuse a peer that runs different rules. The p2p
protocol version stays 1 and the state version stays 2, so a binary that executes a
changed rule and one that does not peer normally, and fork at the first block that
reaches the rule. The only protection is discipline, and this page is that discipline
written down (ledger PL-R5T-30, PL-R5T-16, PL-DC-04).

## The rule

A change to what a block leaves behind, or to which transaction or block is valid, is a
**chain rule**. A chain rule ships:

1. **Behind an activation height, off by default.** A top-level setting named
   `…ActivationHeight` (or `…FromEpoch` / `…FromUnix` where the rule is epoch- or
   time-based). `0` is off. The shipped `config.toml` carries `0`.
2. **Pinned off in four places**, so a release cannot switch it on by accident:
   `config.toml`, `cmd/nhb-snapshot/pins.go` (`liveChainRules`; `check-config` refuses a
   node started from a snapshot with any other value), `tests/config`
   (`TestShippedConfigConsensusValuesMatchTheGenesis`), and the loader test in `config/`.
3. **Carried by the state processor copies and the rebuild path**
   (`StateProcessor.Copy`, `Node.rebuildStateProcessorLocked`): a copy that drops the
   setting applies the rule from height 0.
4. **Listed in `docs/consensus/invariants.md`** with the sentence "must be identical on
   every validator" and what it changes.
5. **Covered by the consensus digest** (below). A test fails if a setting is pinned
   but missing from the digest, or if a new top-level setting is neither digested nor
   declared node-local.
6. **Proven twice before it is proposed for a release**: a replay of real blocks with
   the setting off must be byte-identical to the previous release (hash and state root
   at every height), and a local network of real processes with the setting on must
   cross the height without a state-root mismatch on any node. A deliberate-breakage
   run of the new code must catch every non-equivalent breakage.

## Choosing and changing a height

- Choose the height at least one day of blocks above the tip when both validators have
  been restarted with it (at 2-second blocks, about 43,200 blocks), never closer.
- The same height on both validators, and the same binary. Restart them in the same
  second, just after an epoch close.
- Compare the digests before the height (below). Do not rely on having typed the same
  number twice.
- Before the tip reaches the height, the setting may be moved or set back to `0` by
  restarting both validators together. After any node has executed the height, the
  setting is part of history and is never changed again. A wrong value after that is
  repaired by a new rule behind a new height, not by editing the old one.
- A setting is never given a height at or below the current tip: that rewrites blocks
  already committed.

## The consensus digest (the parity check)

Every node, at start-up, hashes the effective settings (after defaults) that two
validators must hold alike and publishes the result:

- the log line `Consensus settings digest` carries the **root** and one hash per
  **section** (`chain-rules`, `governance`, `potso`, `swap`, `lending`, `subscriptions`,
  one `global.*` section per part of the global state-machine settings, and
  `environment`);
- the same text is written to `consensus_config_digest.txt` in the data directory;
- the root is sent in the p2p handshake. A peer whose root differs is reported at error
  level in the log. It is advisory: the field is outside the handshake signature, a
  build that does not know it ignores it, and no peer is refused or penalised for it;
- `nhb-snapshot config-digest --config <file>` prints the digest of a config file, and
  `nhb-snapshot check-parity --a <file> --b <file>` compares two validators' files and
  names the sections that differ. Only hashes are written, logged or sent, never a
  value.

The environment section carries `NHB_MASTER_TREASURY`, which replaces the genesis admin
wallet on the node that has it set. A node with it set logs an error at start-up and
still starts.

The round timers (`bft-timing`) are reported but are not part of the root: they change
when a validator moves on, not what a block holds.

What the digest does **not** do: it does not make the validators agree (nothing moves
into genesis or chain state), it does not stop a node from starting with different
settings, and a settings difference that is not in a digested section is not seen. The
structural cure, a compile-time activation table per chain instead of per-node
configuration, is not built. A bump of the p2p protocol version, which would make
mismatched builds refuse each other, is not done either: it would make every mixed
rollout a hard stop and is the owner's decision.

## Checklist for a release that carries chain rules

1. `go test ./config ./cmd/nhb-snapshot ./tests/config` pass (the digest golden file
   `config/testdata/shipped_consensus_digest.golden` is updated in the same commit as
   any change to the shipped settings, on purpose).
2. Replay with every new setting off equals the previous release on the same blocks.
3. Local network with each setting on crosses its height cleanly.
4. Both validators started with the same binary; `check-parity` on their two digest
   files prints `ok`; the root is the same in both logs.
5. Heights chosen as above, written down before the restart.
