# Onboarding a Validator From a Snapshot

This is the way to add a new validator, or any new full node, to the network
that started on 2026-09-09. **Block sync from genesis is not supported on that
network** and a node started from an empty data directory will not sync it
(see [Why not from genesis](#why-block-sync-from-genesis-is-not-supported)). A new
node starts from a verified copy of the chain database of a node that is
already running, syncs the blocks that came after it, and only then registers.

Everything below is backed by code in this repository or by a test that is
named where it is used. What could not be tested is listed in
[What was tested](#what-was-tested-and-what-was-not).

## Contents

- [What you need](#what-you-need)
- [Quick start](#quick-start)
- [The identifiers everything is pinned to](#the-identifiers-everything-is-pinned-to)
- [What the script does and checks](#what-the-script-does-and-checks)
- [Running it again, and --reset-state](#running-it-again-and---reset-state)
- [What a snapshot host cannot make the script do](#what-a-snapshot-host-cannot-make-the-script-do)
- [What a snapshot proves, and what it does not](#what-a-snapshot-proves-and-what-it-does-not)
- [Making snapshots](#making-snapshots)
- [Snapshot age and how large a gap a follower can close](#snapshot-age-and-how-large-a-gap-a-follower-can-close)
- [Never copy another validator's identity](#never-copy-another-validators-identity)
- [Doing it by hand](#doing-it-by-hand)
- [Troubleshooting](#troubleshooting)
- [Why block sync from genesis is not supported](#why-block-sync-from-genesis-is-not-supported)
- [What was tested, and what was not](#what-was-tested-and-what-was-not)
- [Reference: nhb-snapshot](#reference-nhb-snapshot)

## What you need

- An Ubuntu server as described in the [onboarding guide](onboarding.md)
  (sizing, firewall, and the 10,000 ZNHB you will need later).
- **The location of a snapshot** (a directory URL that holds `manifest.json`
  and the archive it names) and **a bootnode** (`host:port` of a peer) from
  whoever operates the network. Neither is built into the script: which
  snapshots you trust is your decision, and the script has no default host.
- Optionally the RPC URL of a node you trust (`--tip-rpc`), used only to decide
  when your node has caught up.
- The source commit the snapshot was taken with. The manifest names it
  (`nhb-snapshot manifest show --manifest manifest.json --field producer.binaryCommit`).
  Your node executes every later block with your own build, so it has to be the
  same consensus code: check that commit out before you run the script.

## Quick start

```bash
git clone https://github.com/josephblackelite/nhbchain.git
cd nhbchain
git checkout COMMIT_NAMED_IN_THE_MANIFEST

bash scripts/validator-only-bootstrap.sh \
  --beneficiary YOUR_NHB_WALLET_ADDRESS \
  --snapshot-url https://SNAPSHOT-HOST.example/PATH \
  --bootnode BOOTNODE-HOST.example:6001 \
  --tip-rpc https://TRUSTED-RPC-HOST.example      # optional
```

`SNAPSHOT-HOST.example`, `BOOTNODE-HOST.example` and `TRUSTED-RPC-HOST.example`
are placeholders. `scripts/validator-only-bootstrap.sh` is a five-line wrapper
around `scripts/deployvalidator.sh`; both take the same flags. Run it again at
any time: it is safe to repeat (see
[Running it again](#running-it-again-and---reset-state)).

When it finishes the node is running as a follower at the network tip, its
registration has been submitted, and it prints what to do next (delegate or
self-stake at least 10,000 ZNHB; see the [onboarding guide](onboarding.md)).
Until that stake is in and an epoch boundary has passed, the node does not vote.

The flags:

| Flag | Meaning |
|---|---|
| `--beneficiary` | Required. Wallet that receives this validator's epoch reward payouts. |
| `--snapshot-url` | Required (or `NHB_SNAPSHOT_URL`). Directory URL holding `manifest.json` and the archive. `https` only unless `--allow-insecure-http`. |
| `--bootnode` | Required (or `NHB_BOOTNODE`). Plain `host:port`, never an `enode://` URI. |
| `--tip-rpc` | Optional (or `NHB_TIP_RPC_URL`). A node you trust. Used to decide when you have caught up, and to refuse a node whose newest blocks are not that node's. |
| `--max-lag-blocks` | How far from that node (either side) still counts as caught up. Default 3, at most 15. |
| `--sync-timeout` | Seconds to wait for the catch-up. Default 7200. |
| `--max-snapshot-age` | Refuse a snapshot created longer ago than this, for example `48h`. |
| `--tip-hash`, `--state-root` | Optional. The tip hash and state root the snapshot must have, 32 bytes of hex each, as read from nodes you trust. A snapshot with another tip is refused before it is downloaded. |
| `--max-snapshot-gib` | Refuse a snapshot whose archive, or whose unpacked database, is larger than this many GiB, or that the disk has no room for. Default 16. |
| `--listen-addr`, `--rpc-addr`, `--external-address` | As before. |
| `--reset-state` | Replace the data directory with a snapshot: the snapshot is downloaded, verified and unpacked first, and only then is the node stopped and the old directory moved aside (never deleted). Keeps the node's p2p identity and vote state. Works on a node that is already a validator. |
| `--allow-insecure-http` | Accept an `http://` or `file://` snapshot URL. |
| `--allow-existing-key` | Use a key that was already in `/etc/nhbchain/validator.key` but was not made by this script. |
| `--allow-binary-mismatch` | Continue although the node built here is not the one the snapshot was taken with. |

`--email` and `--onboarding-email-endpoint` no longer exist, and the bootnode has
no default. The flow that started from an empty data directory and synced
history (including `--reset-state` as the way to start fresh) is gone.

## The identifiers everything is pinned to

The script and the tool refuse anything that does not match these. They are
constants in `scripts/deployvalidator.sh`, and `tests/config/shipped_genesis_test.go`
checks them against the genesis file.

| Identifier | Value |
|---|---|
| Chain id (`net_info` `chainId`; the first 8 bytes of the genesis hash) | `18346390202490284624` |
| Genesis block hash | `0xfe9b78af9223ea50f456f63c41084dd99bac4aaa3a790a10fcc26d1dc63210a2` |
| Genesis file | `config/genesis.relaunch.json`, sha256 `10932798a0058ae35b135dae1a6ee1bdf6a8bc528a55c1eeb3e9eaab534f4b3b` |

The chain id is what identifies the network. The `NetworkId` in `config.toml`
is not consulted for that: `cmd/nhb/main.go` builds the peer-to-peer settings
from `node.ChainID()`, which comes from the genesis block. The README's "Network
Connection Details" gives the same chain id.

Values in `config.toml` that change what a block does, and so must be the
network's (the script checks them with `nhb-snapshot check-config`, which reads
the treasuries from the genesis file so a config can only pass by agreeing with
it):

| Key | Value |
|---|---|
| `[potso.rewards] TreasuryAddress` | the genesis loyalty treasury, `znhb1spruw63528zhhys2zxfgu2yf5ulcrlclx6hfhn` |
| `[global.Fees] OwnerWallet` and `[[global.Fees.Assets]]` NHB `OwnerWallet` | the genesis `adminWallet`, `nhb1spruw63528zhhys2zxfgu2yf5ulcrlcltg3zdj` |
| `[[global.Fees.Assets]]` ZNHB `OwnerWallet` | `znhb1spruw63528zhhys2zxfgu2yf5ulcrlclx6hfhn` |
| `[subscriptions] Treasury` | `nhb1spruw63528zhhys2zxfgu2yf5ulcrlcltg3zdj` |
| `QuorumCertActivationHeight` | `0` |

Until this change `config.toml` named the treasury of the network that came
before this one in all of these places. A node started with it executes the
same blocks to a different state. The test
`TestFollowerWithDriftedConfigDiverges` in `cmd/nhb-snapshot` shows it happening
on a local network (it stops at the first reward epoch after the snapshot).

Also required, and checked by the script:

- `NHB_MASTER_TREASURY` must not be set. A node that points it at any other
  address than the genesis `adminWallet` computes different state
  (`core/node.go`, `NewNode`).
- The node binary must be the one the snapshot was taken with: the same binary
  hash, or the same source commit.
- `QuorumCertActivationHeight = 0` leaves quorum-certificate verification of
  synced blocks off (`cmd/nhb/main.go` only sets it when it is above zero, and the
  node's default is "never"), exactly as on the live validators. Your node still
  executes every block and rejects one whose state root is not the one in the
  header.

## What the script does and checks

Each step stops the script with a message when it fails. Nothing is skipped
quietly, and no secret is printed.

1. **Validates its arguments before touching the system**: an `https` snapshot
   URL, a `host:port` bootnode, an `nhb1...` beneficiary, no
   `NHB_MASTER_TREASURY` in the environment.
   (`TestDeployArgumentChecksRunBeforeAnythingIsTouched`)
2. **Installs the tools and builds** `nhb`, `nhb-cli` and `nhb-snapshot`, after
   checking that `config/genesis.relaunch.json` is byte for byte the live genesis.
3. **When it will install a snapshot** (the data directory holds nothing, or
   `--reset-state` was given), **fetches only the manifest first** and checks that
   it is for the pinned chain id and genesis hash (and for the tip hash and state
   root you pinned, if you did) and that the node built here is the one the
   snapshot was taken with. A node that already holds the chain gets none of this
   (see [Running it again](#running-it-again-and---reset-state)).
   (`TestDeployInstallsAVerifiedSnapshotAndRefusesTheRest`,
   `TestDeployBinaryIdentityPolicy`, `TestDeployPinsTheSnapshotsTipAndStateRoot`)
4. **Makes this validator's key on this machine** and never accepts one. It
   refuses to go on when another `nhb` process is running outside `nhb.service`,
   or when the key file was not made by this script and this host has no node data
   (`--allow-existing-key` overrides the second, at your responsibility).
   (`TestDeployRefusesASecondNodeAndAForeignKey`)
5. **Downloads and verifies the snapshot** within the
   [size limits](#what-a-snapshot-host-cannot-make-the-script-do), then unpacks it
   (see the checks below), and refuses to do so when the key is already a
   validator in the snapshot's state (a first run only: see `--reset-state`). It
   never writes into a data directory that already has data.
6. **Writes the node config** from the shipped `config.toml`, changing only the
   listen and RPC addresses, the data directory, the genesis path, the key
   source, the network name, the external address and the peers, and runs
   `nhb-snapshot check-config` on the result.
   (`TestDeployRenderConfigChangesOnlyNodeLocalValues`,
   `TestDeployRenderConfigFailsLoudlyOnAStaleTemplateAndRefusesDrift`)
7. **Starts `nhb.service`** as a follower, or leaves it running when nothing
   changed, and **waits until the node is at the network tip**, checking on the
   first answer that it reports the pinned chain id and genesis hash. At the tip
   means all of these, on two polls in a row:
   - the node is connected to at least one peer;
   - when this run installed a snapshot, the node has applied a block **above the
     snapshot's height**. A snapshot that is not part of the network's chain can
     never be followed past, so this is what a forged snapshot cannot fake, and a
     node that never reached a peer (a wrong `--bootnode`) cannot either;
   - with `--tip-rpc`, its height is within `--max-lag-blocks` of that node's on
     either side, and the newest blocks the two nodes hold are **the same
     blocks** (a different block hash at a height both hold ends the run: the
     node is on another chain);
   - without `--tip-rpc`, its newest block is dated within a minute of this
     host's clock, on either side (a block dated in the future says nothing).

   It stops with a diagnosis when the node makes no progress for 15 minutes or
   the timeout passes.
   (`TestDeployWaitUntilSynced`, `TestDeployWaitUntilSyncedNeedsBlocksPastTheSnapshot`,
   `TestWaitSynced*`, `TestAForgedSnapshotIsRefusedAndAnUnconnectedNodeOnItIsNeverAtTheTip`)
8. **Only then** submits the reward beneficiary and the zero-value registration
   and prints the next steps.

What the unpacking checks (all in `nhb-snapshot extract`; every one has a test
in `cmd/nhb-snapshot/snapshot_test.go`):

- the archive's size and sha256 against the manifest, before anything is written;
- the manifest against the chain id and genesis hash you pin (and, if you give
  them, a minimum height, a maximum age, a tip hash and a state root);
- the archive itself: gzip and tar only, regular files only, exactly the files
  the manifest lists with the sizes and hashes it gives, and only the names of
  chain database files (`CURRENT`, `MANIFEST-*`, `*.log`, `*.ldb`, `*.sst`). A path
  with `..`, an absolute path, a drive letter, a nested path, a link, a device, a
  named pipe, a directory entry, a repeated name, a file the manifest does not
  list, a file larger than it says, data after the end of the archive, or
  expansion beyond the manifest is refused. Key and identity files are refused
  by name even when the manifest lists them;
- that the target directory does not exist or is empty. The archive is unpacked
  into a new directory next to it and only moved into place at the end, so a
  refused snapshot leaves nothing behind;
- that what was unpacked opens read-only as a chain database: every table file
  the MANIFEST refers to is present and as long as it says; the tip, the height
  index and the genesis agree; the newest 256 headers hash and link to their
  parents; the tip's state root is present and **every node of the state trie
  re-hashes to its address** (so a state altered under an unchanged root is
  refused); and the chain id, genesis hash, height, tip hash, state root and tip
  time equal what the manifest claims.

## Running it again, and --reset-state

The script is meant to be run again: after a `git pull`, after a reboot, or
because an earlier run stopped part of the way.

- **A node whose data directory already holds this network's chain is never
  given a snapshot**, so such a run does not fetch a manifest, does not compare
  the node built here with the snapshot's binary, and does not need the snapshot
  host at all. What the host publishes now (a release, or the daily refresh,
  replaces `manifest.json` with a snapshot of another binary), or whether it
  answers, cannot change that run. It builds, checks the config, restarts the
  node only when its binary, config or key changed, waits for the tip and
  registers, as always.
  (`TestDeployRerunDoesNotDependOnTheSnapshotHost`)
- A run that does install a snapshot (an empty data directory, or `--reset-state`)
  fetches the manifest and checks the binary before anything else on the host
  changes: the key, the config, the data and the service are all untouched until
  that check has passed. (It follows the build, because it compares the binary the
  build made.) (`TestDeployResetStateChecksTheBinaryBeforeItTouchesTheNode`)
- Whether the data directory and the key are there is asked as root. Both belong
  to the service user and are closed to the user who runs the script, who would
  otherwise see an empty directory and no key.
  (`TestDeployLooksForTheDataAndTheKeyAsRoot`)

`--reset-state` is for a node whose data directory is broken, and that includes
a node that is already a registered validator. It works in this order:

1. The snapshot is downloaded, verified and unpacked into a new directory next to
   the old one (`nhb-data.new-<time>-<pid>`) while the node keeps running on the
   old one. Whatever is wrong with the snapshot ends the run there, with nothing
   changed: the node is not stopped, no directory is moved, and the message says
   so. (`TestDeployResetStateWithASnapshotThatDoesNotCheckOutChangesNothing`)
2. Only then is the node stopped, whatever state it is in (a node whose data
   directory is broken is often restarting every few seconds rather than running,
   and a restart in the middle of the swap would start it on whatever the
   directory holds then), and its p2p identity and its vote and lock state
   (`p2p/node_key.json`, `bft_sign_state.json`, `polc_lock.json`) copied into the
   new directory. They are read after the stop, so what the node voted while it
   stopped is what carries over.
3. The old directory is moved aside to `nhb-data.replaced-<time>-<pid>` (it is
   never deleted; delete it yourself when you no longer need it) and the new one
   is moved into its place.
   (`TestDeployResetStateOfARegisteredValidator`)

The node's own key is, by then, a validator in every snapshot, so the check that
refuses a new node's key is not made for the node's own data directory. A first
run on a host with no data still makes it, with or without `--reset-state`
(`TestDeployResetStateWithNoDataStillRefusesAKeyThatIsAlreadyAValidator`).

If a step of the swap fails, the old directory is put back and the message says
where every directory is and whether `nhb.service` is stopped, with the command to
start it. If the old directory cannot be put back, the message says where it is and
prints the command that moves it back.
(`TestDeployResetStateSwapFailureIsReportedTruthfully`)

## What a snapshot host cannot make the script do

A manifest is not signed, so nothing in it is trusted, including its sizes. The
real snapshot is a few hundred megabytes; the script and the tool bound what a
host can ask for:

- The script accepts an archive of at most `--max-snapshot-gib` GiB (default 16)
  and one that unpacks to at most that, and refuses the rest **before it
  downloads anything**. curl is given the declared size as its cap, so a host
  that sends more is cut off at it, and what was written is removed.
- Before the download it checks that the disk holds the archive, what it unpacks
  to, and 1 GiB for the node. `verify` and `extract` are both given the same
  unpack bound (`--max-bytes`), so neither reads or writes more than the operator
  allowed.
- The tool refuses a manifest that describes an archive over 64 GiB, one that
  unpacks to over 64 GiB, or one that claims to unpack to more than 64 times its
  size. The snapshots measured while testing (chains built for the tests, and the
  local network's, 862 kB unpacking to 1.67 MB) unpack to 1.2 to 2.2 times their
  archive, and the live chain's was not measured here: an archive of a few
  megabytes that says it holds gigabytes is a decompression bomb, and it is
  refused for what it says before a byte of it is unpacked.
- It refuses a snapshot whose newest block is dated more than five minutes ahead
  of this host's clock: a block cannot be in a snapshot before it exists.

(`TestDeployRefusesASnapshotBeyondWhatTheOperatorAccepts`,
`TestDeployChecksThereIsRoomForTheSnapshotBeforeItDownloadsIt`,
`TestDeployBoundsWhatVerifyAndExtractUnpack`,
`TestDeployDoesNotKeepMoreThanTheManifestDeclaredFromAHostThatSendsMore`,
`TestManifestBoundsWhatItCanAskAConsumerFor`,
`TestExtractRefusesADecompressionBombBeforeWritingAnything`,
`TestExtractToleratesClockSkewButNotAnInventedFuture`)

## What a snapshot proves, and what it does not

- **The state is checked against the header.** Whoever hosts the snapshot cannot
  hand you a different state than the one the tip header commits to.
- **The tip is checked by the network.** The first block your node syncs must
  link to the snapshot's tip hash, and every block after it must reproduce the
  state root in its header. A snapshot that is not a prefix of the real chain
  cannot sync; one that is, is verified from then on by executing the chain.
  That is why the script does not accept a node's own word that it is at the tip:
  it requires the node to have a peer and to have applied a block above the
  snapshot's height, and, with `--tip-rpc`, to hold the same newest blocks as that
  node. A forged snapshot (the live genesis, valid links, a new state and no
  signatures of anyone: there is nothing in a snapshot to check a signature of) is
  accepted by `extract`, and then never gets there.
  (`TestAForgedSnapshotIsRefusedAndAnUnconnectedNodeOnItIsNeverAtTheTip`)
- **History below the tip is trusted.** Old blocks and old state are checked for
  consistency (headers link and hash for the newest 256 blocks, the whole height
  index is present), not against the network. Use a snapshot from a source you
  trust, and compare its `tipHash` and `stateRoot` with two nodes you trust
  before you register (`nhb_getLatestBlocks` shows the newest blocks; a
  snapshot's tip is normally beyond that window by the time you read it, so
  compare after your follower has synced the blocks in between). Or pin them
  before the download: read the tip hash and state root of the snapshot's block
  from nodes you trust and pass them as `--tip-hash` and `--state-root`; the
  script refuses a snapshot that is not that block before it downloads it, and
  `verify` and `extract` check them again.
- **Quorum certificates are not verified on the sync path** with the shipped
  configuration (see above). Sync from a bootnode you trust.
- The manifest is not signed. Fetch it over `https` from a location you control
  or trust.

## Making snapshots

`scripts/make-snapshot.sh` runs on a host that runs a validator or a follower,
as root or as the node's user. It only reads the node's data directory. It never
stops, signals, locks or writes to the node.

```bash
bash scripts/make-snapshot.sh \
  --data-dir /var/lib/nhbchain/nhb-data \
  --out-dir /var/lib/nhbchain/snapshots
```

It writes into `--out-dir`, in this order, an archive
`nhb-snapshot-<genesis prefix>-h<height>.tar.gz`, its manifest, and the same
manifest as `manifest.json` (the fixed name a new node fetches). Uploading them is
a separate step and is your decision: where snapshots are hosted is not part of
this repository.

How the copy is made:

1. The chain database is a LevelDB directory. Its table files (`*.ldb`) never
   change once written, so they are hard-linked into a private staging directory
   (copied when a link is not possible). The MANIFEST and the journal change
   while the node runs, so they are copied, last.
2. A pass counts only if nothing changed underneath it: the same `CURRENT`, the
   same MANIFEST at the same size, the same set of journals before and after, every
   table copied. The MANIFEST size is read before the tables are listed, so a
   table it refers to cannot be missing from the list. Passes repeat until two
   consecutive ones agree.
3. The staged copy is opened read-only by `nhb-snapshot info`, which prints and
   checks the chain id, genesis hash, height, tip hash and state root and
   re-hashes the whole state trie. A copy that does not open is never packed. A
   copy that lacks a table file the MANIFEST refers to, or holds one of the wrong
   size, is never packed either; a table nothing refers to (one a compaction was
   still writing) is left out.
4. It is packed deterministically (sorted names, fixed owner, mode and time, no
   gzip name or time; the same files always give the same archive with one build
   of the tool), written with
   its manifest, unpacked again and opened again, and only then published.

Which files are in a snapshot: `CURRENT`, `MANIFEST-*`, `*.log`, `*.ldb`,
`*.sst`, chosen by name from that allow-list and nothing else. They hold
blocks, the state trie and indexes: data every node already has and that is
public on chain. No key, and nothing that identifies the node.

What is never read, copied or packed, and why it matters:

| Not included | Why |
|---|---|
| `p2p/node_key.json` | The node's network identity (a private key). Two nodes with one identity are indistinguishable to their peers. |
| `p2p/peerstore/` | The addresses of the peers this node has met. |
| `bft_sign_state.json` | What this validator has voted, so a restart cannot make it sign a conflicting vote. It belongs to one key. |
| `polc_lock.json` | This validator's consensus lock. It belongs to one node. |
| `genesis.resolved.json` | Regenerated from the genesis file at every start. |
| `LOCK`, `LOG`, `LOG.old`, `CURRENT.bak`, temporary files | LevelDB and node bookkeeping. |
| The consensus key | In the deployment this repository sets up it is not in the data directory: the node reads it from the environment (`NHB_VALIDATOR_RAW_KEY`, set from `/etc/nhbchain/node.env`). A key file someone put in the data directory anyway is not on the allow-list and is never read. |

The script also refuses a staged copy that holds a name that looks like key
material, and the packer refuses any file that is not on the allow-list.
`TestSnapshotOnboardingEndToEnd` searches the archive of a running validator for
its consensus key, its p2p identity key, the RPC secret and the names of those
files, in hex and as raw bytes, and finds none.

The manifest records the chain id, genesis hash, height, tip hash, state root,
the tip's block time, when it was made, the binary it was taken with (its
sha256, its source commit and version, from `--node-binary`, `--binary-commit`
and `--binary-version`, or from the checkout the binary was built in), and the
archive's name, size, sha256 and the size and sha256 of every file in it.

## Snapshot age and how large a gap a follower can close

A follower closes the gap between its snapshot and the network tip by asking a
peer for blocks and executing each one. Three things bound how large a gap it can
close. The first two come from the code, the third was measured.

1. **The request budget of the node it asks.** A node answers at most 4 block
   requests per second per remote address (a burst of 16) and 32 per second (a
   burst of 64) across all addresses, and one answer holds at most 128 blocks or
   512 KiB (`p2p/server.go`: `getBlocksRatePerIP` and the constants after it, and
   `admitRequest`; `core/node.go`: `networkBlockSyncBatchSize` and
   `networkBlockSyncMaxBytes`). One peer therefore serves a follower at most 512
   blocks per second, after a first burst of 2,048. A request over the budget is
   dropped without blaming the follower; the serving node logs `Dropping chain
   data requests from a peer over its budget`. Peers listed as persistent peers of
   the serving node are not limited, and a new node is not one.
2. **That the follower asks again.** It asks for the next batch as soon as it has
   applied one, and again whenever a block or a status report from a peer shows
   that it is behind (`core/node.go`, `handleNetworkBlocks` and
   `handleNetworkStatus`). While the chain produces blocks, every new block makes
   good a dropped request. On a chain that has stopped nothing does: on the local
   network a follower stalled about 3,000 blocks after its snapshot (twice, while
   the serving node logged the over-budget drops) and went on as soon as the chain
   produced a block. Do not judge a follower's progress while the network is
   halted.
3. **How fast the host executes blocks.** The follower re-executes every block
   and checks its state root against the header.

Measured on the local network (`TestSnapshotGapCatchUp`, `NHB_SNAPSHOT_E2E_GAP=1`):
one Windows machine ran the validator, the serving node and the follower; the
serving node and the follower used the shipped consensus timeouts, so the chain
produced a block about every 5 seconds while they synced, as the live chain does;
about one block in forty carried a transaction (transfers, heartbeats, stake
changes). Each follower started from a snapshot taken from the running validator
at a different height and was timed from its first synced block to the height the
chain had when the snapshots were compared:

| Blocks between snapshot and tip | Time to close it | Rate |
|---|---|---|
| 948 | 0.6 s | 1,557 blocks/s (inside the first burst) |
| 4,940 | 8.4 s | 585 blocks/s |
| 19,944 | 42.0 s | 474 blocks/s |
| 59,847 | 135.8 s | 441 blocks/s |

Every one of them reached the tip with the block hash and state root of the
serving node at every height, and none was refused or stalled. The rate settles at
the request budget (4 requests of 128 blocks per second is 512 blocks per second);
the blocks here are light, so it was the budget and not execution that set it. No
gap size was found at which a follower fails, up to 59,847 blocks, which is about
2.8 days of the live chain (one block roughly every 4 seconds, 21,600 a day).

What that means for the live chain, read with care:

- At the measured 440 to 590 blocks per second a day's gap takes under a minute
  and a week's about six minutes, **if the host executes blocks as fast as they
  are served**. The live chain's blocks are heavier than these. The replay
  investigation replayed all 214,330 live blocks through the same commit path at 73
  blocks per second (49 minutes at 30% CPU); at that speed a day is about five
  minutes and a week about thirty-five. The gap is therefore a matter of minutes to
  an hour, not of possibility.
- **The real limit is the binary.** Your build has to execute every block after
  the snapshot. A snapshot taken before a release that changes what blocks do
  cannot be caught up with a build from after it, and a build from before it
  cannot go past it. Take a fresh snapshot after every such release (the manifest
  records the binary), and refuse older ones with `--max-snapshot-age`.
- **Refresh snapshots at least daily**, and set `--max-snapshot-age 72h` (or
  shorter) for new nodes. A daily cadence keeps every catch-up to a few minutes and
  keeps the snapshot from predating a release. The schedule is yours to set.
- If a snapshot is stale, get a newer one: run the script again with
  `--reset-state` and a fresher `--snapshot-url`. Do not start from an empty data
  directory.

## Never copy another validator's identity

A data directory copied from another validator must never keep that validator's
p2p identity or consensus key.

- `make-snapshot.sh` never copies `p2p/node_key.json`, the peer store, the
  vote-state and lock files, or any key, and `nhb-snapshot extract` refuses to
  unpack any of them from an archive.
- The consensus key is not in the data directory. The script makes a new one on
  the new host (`nhb-cli generate-key`, mode `0600`) and never takes one as an
  argument, so it never enters shell history or a process list.
- The script refuses to start from a snapshot in which the new key is already a
  validator, refuses a second `nhb` process on the host, and refuses a key file it
  did not make on a host with no node data.
- **Do not** copy `/etc/nhbchain/validator.key`, `/etc/nhbchain/node.env` or a
  node's whole data directory to another machine "to save time". A key that runs
  twice signs two different votes for one height, which is what the network slashes.
  A copied `p2p/node_key.json` makes two nodes indistinguishable to their peers.
  Use a snapshot made by `make-snapshot.sh`.
- `--reset-state` keeps the identity and vote state of the node it is run on,
  because that is the same node, and is the one case where the snapshot may list
  the node's own key as a validator. It unpacks the new snapshot first, stops the
  node, copies the identity and vote state over, moves the old data directory
  aside (it never deletes it) and moves the new one into place.

## Doing it by hand

What the script does, without it (Linux; adjust paths):

```bash
# 1. Build from the commit named in the manifest.
go build -o bin/nhb ./cmd/nhb
go build -o bin/nhb-cli ./cmd/nhb-cli
go build -o bin/nhb-snapshot ./cmd/nhb-snapshot

# 2. Fetch the manifest, then the archive it names (https).
curl -fsSLO https://SNAPSHOT-HOST.example/PATH/manifest.json
ARCHIVE=$(bin/nhb-snapshot manifest show --manifest manifest.json --field archive.name)
curl -fsSLO "https://SNAPSHOT-HOST.example/PATH/${ARCHIVE}"

# 3. Verify against the pinned identity, then unpack.
export NHB_SNAPSHOT_CHAIN_ID=18346390202490284624
export NHB_SNAPSHOT_GENESIS_HASH=0xfe9b78af9223ea50f456f63c41084dd99bac4aaa3a790a10fcc26d1dc63210a2
bin/nhb-snapshot verify --manifest manifest.json --archive "$ARCHIVE"
bin/nhb-snapshot extract --manifest manifest.json --archive "$ARCHIVE" \
  --target /var/lib/nhbchain/nhb-data \
  --reject-validator "$(bin/nhb-cli address /etc/nhbchain/validator.key | grep -o 'nhb1[a-z0-9]*')"

# 4. Config: copy config.toml, change only the node-local keys, then check it.
bin/nhb-snapshot check-config --config /etc/nhbchain/config.toml --genesis config/genesis.relaunch.json

# 5. Start the node and wait for the tip.
sudo systemctl start nhb.service
bin/nhb-snapshot wait-synced --rpc http://127.0.0.1:8545 --tip-rpc https://TRUSTED-RPC-HOST.example
```

## Troubleshooting

| You see | It means |
|---|---|
| `the snapshot is for chain ... not the pinned network` | The manifest is for another network. Nothing was installed. |
| `the node built here is not the one the snapshot was taken with` | Your build differs from the snapshot's. `git checkout` the commit it prints and run the script again. `--allow-binary-mismatch` is only for two builds that differ in nothing consensus executes. |
| `the archive sha256 is ... the manifest says ...` or `a truncated or replaced download` | A damaged or replaced download. Fetch it again. |
| `the archive entry ... is not a chain database file name` and similar | The archive is not a snapshot made by `make-snapshot.sh`. Do not use it. |
| `the unpacked snapshot does not open` | The archive is genuine but the database inside is damaged or incomplete. Ask for another snapshot. |
| `... is a validator in this snapshot's state` | On a first run: the key on this host is already a validator. It must not start a second node. (`--reset-state` on a node's own data directory does not make this check.) |
| `the node did not reach the network tip within ...` | Still catching up: raise `--sync-timeout`, or the snapshot is too old (see the measured gap above). |
| `the node has stopped making progress` | No new block for 15 minutes while behind. Check `journalctl -u nhb.service`, the bootnode, and that this host's clock is right. |
| `connected to no peer` or `no block above the snapshot's height N has been applied yet` in the wait output | The node has not heard from the network, or has not applied one block past its snapshot. Check the `--bootnode` address and the firewall (6001 TCP and UDP), and that the snapshot is one of the network's. |
| `the node's newest blocks are not the ones the --tip-rpc node has` | The node's chain and the reference node's differ: the snapshot is not part of the network's chain, or one of the two nodes is on a fork. Do not register. Get a snapshot from a source you trust. |
| `the snapshot archive is ... more than the ... GiB this run accepts`, or `the snapshot unpacks to ...` | The manifest asks for more than `--max-snapshot-gib` allows. Nothing was downloaded. If the network's snapshot has really grown that large, raise the flag; otherwise do not use that host. |
| `not enough free disk space in ...` | The disk cannot hold the archive, what it unpacks to and 1 GiB. Nothing was downloaded. |
| `the snapshot's newest block is dated ... ahead of this host's clock` | The snapshot's tip is in the future: this host's clock is wrong, or the snapshot is made up. |
| `... a decompression bomb` | The manifest says the archive unpacks to more than 64 times its size. It is not a snapshot. |
| `the snapshot's tip hash is ..., not the ... pinned with --tip-hash` (or `--state-root`) | The snapshot is not the block you pinned. Nothing was downloaded. |
| `the snapshot could not be installed; nothing was changed` | The unpacked database did not open or match its manifest (or, on a first run, the key is already a validator). The data directory and `nhb.service` are exactly as they were. |
| `state root mismatch` in the node's log | The node computes different state than the network: a config that differs in a consensus-relevant value, a different build, or `NHB_MASTER_TREASURY`. Run `nhb-snapshot check-config`. |
| `Dropping chain data requests from a peer over its budget` in the serving node's log, and your follower stops after a few thousand blocks | The serving node's request budget (see the gap section). Expected. The follower goes on with the next block the chain produces; a halted network produces none. |
| The node is at height 0 and the log shows no progress after an earlier attempt to sync from genesis | That attempt could not sync (see below). Run the script again with `--reset-state`. |
| `vote from non-validator` in a **validator's** log | Your follower's consensus engine votes like any node and the validators reject the vote because its key is not in the validator set. Expected, harmless (the peer is not penalised for it; `p2p/peer.go`), and it stops once the key is a validator. |

## Why block sync from genesis is not supported

The validators ran several different binaries and configurations since block 1,
and five consensus-behaviour changes and four governance-timer configurations were
never height-gated. A build of this release starting from genesis rejects
block 1 and stays at height 0. The replay investigation of 2026-09-20 (ledger
item PL-R1-REPLAY) found:

- The build that produced block 1 seeded the NHB supply counter with a fixed
  constant; the current code skips that seed for this chain id
  (`core/state_transition.go`, `SeedGenesisNHBSupplyOnce`, the NHB-AUDIT-C9 gate).
  It also runs the admin-validator funding reconciliation in the first block,
  where the live chain first ran it at height 439.
- Later divergences follow binary swaps: NHB mint supply tracking (absent until
  height 135726), the redemption request id (`hex(txhash)` before the new form
  took over, between 133528 and 142023), and validator eligibility (a different
  stake basis until 130019).
- Governance proposals store voting periods of 600 seconds and 120 seconds
  where the shipped configuration says 604800 and 172800, because the validators
  ran the short timers in configuration for two periods.
- A clean replay of heights 1 to 214,330 through the peer-sync commit path
  reproduced the live tip hash and state root only with the five behaviours gated
  at the heights above and a per-height governance-timer configuration
  (49 minutes at 30% CPU).

The snapshot RPC methods (`sync_snapshot_export`, `sync_snapshot_import`) and the
package `core/sync` are not a substitute: the same investigation found the export
unsigned with nothing that signs manifests, the governance anchor unable to
verify, and an import that leaves the chain height at zero and is discarded by
the state drift guard at the next block or restart. This procedure does not use
them.

A change that makes genesis sync work would ship the five behaviours as
height-gated code and a chain-level governance-timer table as a release that is
inert above the current tip, or implement checkpoint install. That is a separate
piece of work; until it lands, use a snapshot.

## What was tested, and what was not

Tested, on a local network of real `nhb` processes (Windows, Git Bash):

- `TestSnapshotOnboardingEndToEnd` (`NHB_SNAPSHOT_E2E=1`): a validator started
  from a genesis derived from the shipped one, run for about 2,600 blocks under
  transfers, heartbeats and stake changes and crashed twice so its database has
  table files; a snapshot made from the running node with
  `scripts/make-snapshot.sh`; a second node with another key, other ports and a
  fresh data directory started from it as a follower with the shipped
  `config.toml`; the follower caught up (by the wait the script runs: a peer, a
  block above the snapshot's height, and the validator's newest blocks compared
  hash for hash), followed the chain, survived a crash and restart, its votes were
  rejected by the validator, and its block hash and state root equalled the
  validator's at every height from 0 to 2,585 in the latest run.
- `TestFollowerWithDriftedConfigDiverges` (`NHB_SNAPSHOT_E2E_DRIFT=1`): a follower
  with the shipped config synced from a snapshot at height 1,044; a follower with
  `config.toml` as it was before this change (the treasury of the previous network)
  from the same snapshot rejected block 1,080, the first reward epoch boundary
  after the snapshot, with a state root mismatch, and stayed at height 1,079.
- `TestSnapshotGapCatchUp` (`NHB_SNAPSHOT_E2E_GAP=1`): the table above.
- Unit tests of the tool with hostile archives, of the copy under concurrent
  writes with compactions (`TestMakeSnapshotUnderChurn`), and of the deployment
  script's functions against fake `sudo`, `systemctl` and `ps` and a snapshot of a
  chain with the live genesis.
- The deployment script's `main()` run whole, with only the steps that need a real
  server (packages, the build, the service, the node's RPC) stubbed, against a real
  `nhb-snapshot` and a snapshot host on the loopback interface: a fresh install,
  running it again with the publisher's snapshot replaced or gone, `--reset-state`
  of a node whose own key is a validator, a snapshot that does not check out, a
  failing swap, the size, disk-space and pin refusals, and the sync check
  (`deploy_rerun_test.go`, `deploy_limits_test.go`, `sync_forged_test.go`,
  `snapshot_limits_test.go`).

Not tested here, and the exact commands to run on Linux:

- The system-level steps of `scripts/deployvalidator.sh` (`apt-get`, `useradd`,
  `rsync` to `/opt/nhbchain`, the Go install, the swap file, `systemctl` start and
  restart, `sudo -u nhb`). On a clean Ubuntu 22.04 VM, serve a snapshot directory
  with `python3 -m http.server 8000 --directory SNAPSHOTS` and run
  `bash scripts/deployvalidator.sh --beneficiary nhb1... --snapshot-url http://127.0.0.1:8000 --allow-insecure-http --bootnode BOOTNODE:6001`.
- `TestPackRefusesASymlinkedFile` (symlinks need privileges on Windows) and the
  symlink target case of `TestExtractRefusesBadTargets`:
  `go test ./cmd/nhb-snapshot -count=1` on Linux runs them.
- The producer on ext4 or xfs (hard links were exercised on NTFS):
  `bash scripts/make-snapshot.sh --data-dir DIR --out-dir OUT` against a running node.
- A snapshot of the live chain (334 MB, 214,000 blocks). The live chain's own
  snapshot RPCs and replay were not touched.
- Two things were not proven against the live network and rest on the
  investigation: that a node started from a snapshot of the live chain with the
  shipped configuration follows it (the investigation validated the same path
  with a copy of a validator's data directory and the live configuration), and
  the exact live configuration values (compared with a copy of a validator's
  configuration; secrets are not part of it).

## Reference: nhb-snapshot

```
nhb-snapshot info         --data-dir DIR [--format text|json] [--header-window N] [--no-state-check]
nhb-snapshot pack         --data-dir DIR --out-dir DIR [--binary-version S] [--binary-commit S] [--binary-sha256 HEX] [--latest]
nhb-snapshot verify       --manifest FILE --archive FILE --chain-id N --genesis-hash HEX [--min-height N] [--max-age D] [--tip-hash HEX] [--state-root HEX] [--max-bytes N]
nhb-snapshot extract      --manifest FILE --archive FILE --target DIR --chain-id N --genesis-hash HEX [--reject-validator ADDR] [--max-bytes N] [--tip-hash HEX] [--state-root HEX]
nhb-snapshot manifest show --manifest FILE [--field NAME]
nhb-snapshot check-config --config FILE --genesis FILE
nhb-snapshot wait-synced  --rpc URL [--tip-rpc URL] [--min-height N] [--max-lag-blocks N] [--max-lag-seconds N] [--timeout D] [--stall-timeout D]
```

`--chain-id` and `--genesis-hash` may come from `NHB_SNAPSHOT_CHAIN_ID` and
`NHB_SNAPSHOT_GENESIS_HASH`; the tool has no default for either. It only ever opens
a database read-only, and only a copy: a data directory that a running node holds
open cannot be read (its lock is taken), which is why a snapshot is made from a copy.
`wait-synced` exits 0 when the node is at the tip (see step 7 above: it has a peer;
with `--min-height` it has applied a block above that height; with `--tip-rpc` it is
within `--max-lag-blocks`, at most 15, of that node and holds the same newest
blocks), 3 on timeout, 4 when the node stopped advancing, 5 when the node or the
reference RPC is on another chain, and 6 when the node's blocks differ from the
reference node's. Without `--tip-rpc` it compares the date of the newest block with
this host's clock (`--max-lag-seconds`, default 60, either side), so keep the clock
synchronised. `verify` and `extract` refuse a manifest announcing more than
`--max-bytes` uncompressed bytes (default 64 GiB; the script passes its own, lower,
bound).
