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
- [How long the node takes to start](#how-long-the-node-takes-to-start)
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
- The source commit the snapshot was taken with. The manifest names it in its
  `producer.binaryCommit` field. On a clean machine `nhb-snapshot` does not exist
  yet (the script builds it), so read the field from the manifest's JSON with
  `curl`, which needs nothing built: `curl -fsS https://SNAPSHOT-HOST.example/PATH/manifest.json | sed -n 's/.*"binaryCommit": *"\([0-9a-f]*\)".*/\1/p'`
  (it prints nothing when the manifest names no commit). Your node executes every
  later block with your own build, so it has to be the same consensus code: check
  that commit out before you run the script. **The manifest is not signed, so
  check that this commit is a release you recognise** (compare it with what the
  people who run the network announce) before you check it out: whoever hosts the
  manifest chooses the commit, and an old commit is old consensus code. Pass the
  oldest release you accept as `--min-release-commit` and the script refuses any
  manifest whose commit is not that commit or a descendant of it.
  **That commit has to contain this procedure** (`scripts/deployvalidator.sh` as
  described here and `cmd/nhb-snapshot`), because you run the script from it.
  It does only for a snapshot made by a node whose binary was built from a commit
  that contains them. A snapshot made by a validator that still runs an earlier
  build names a commit whose script starts from an empty data directory and syncs
  from genesis, which does not work on this network. The validators that make
  snapshots have to run a build that contains this procedure first, and
  `make-snapshot.sh` refuses to make a snapshot that does not name its commit (see
  [Making snapshots](#making-snapshots)).

## Quick start

```bash
git clone https://github.com/josephblackelite/nhbchain.git
cd nhbchain
# The commit the snapshot was taken with: the manifest is JSON, and nothing has to
# be built to read it. Check that it is a release you recognise before you use it.
curl -fsS https://SNAPSHOT-HOST.example/PATH/manifest.json | sed -n 's/.*"binaryCommit": *"\([0-9a-f]*\)".*/\1/p'
git checkout COMMIT_NAMED_IN_THE_MANIFEST
ls cmd/nhb-snapshot scripts/make-snapshot.sh   # both must exist: see "What you need"

bash scripts/validator-only-bootstrap.sh \
  --beneficiary YOUR_NHB_WALLET_ADDRESS \
  --snapshot-url https://SNAPSHOT-HOST.example/PATH \
  --bootnode BOOTNODE-HOST.example:6001 \
  --max-snapshot-age 72h \
  --min-release-commit FULL_COMMIT_ID_OF_THE_OLDEST_RELEASE_YOU_ACCEPT \
  --tip-rpc https://TRUSTED-RPC-HOST.example      # optional
```

`SNAPSHOT-HOST.example`, `BOOTNODE-HOST.example` and `TRUSTED-RPC-HOST.example`
are placeholders, and so is the release commit. `--max-snapshot-age 72h` is here
because without it a snapshot of any age is accepted and a stale one fails only
later, with a stall. `scripts/validator-only-bootstrap.sh` is a five-line wrapper
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
| `--min-release-commit` | Optional (or `NHB_MIN_RELEASE_COMMIT`), advised. The full commit id of the oldest release you accept. A manifest whose commit is neither that commit nor a descendant of it is refused, and so is one that names no commit or one your checkout does not have (`git fetch` first). The manifest is not signed, so this is what keeps a snapshot host from steering you to old consensus code. |
| `--tip-rpc` | Optional (or `NHB_TIP_RPC_URL`). A node you trust. Used to decide when you have caught up, and to refuse a node whose newest blocks are not that node's. |
| `--max-lag-blocks` | How far from that node (either side) still counts as caught up. Default 3, at most 15. |
| `--sync-timeout` | Seconds to wait for the catch-up. Default 7200. |
| `--rpc-timeout` | Seconds the node's RPC may stay silent after the service starts. Default 180, which is far above what was measured (see [How long the node takes to start](#how-long-the-node-takes-to-start)). A node that never answers is not running or is crash-looping; the script says so after this long, with the commands that show why, instead of waiting out `--sync-timeout`. (`nhb-snapshot wait-synced` also gives up on a silent node after its `--stall-timeout`, when that is shorter.) |
| `--max-snapshot-age` | Refuse a snapshot whose newest block is older than this; use `72h`. The limit rests on the date of the newest block, which `extract` checks against the database it unpacked; the creation time the manifest states is not signed by anyone and cannot get a stale snapshot past it. Without it a snapshot of any age is accepted. |
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
   When the host has no Go, it installs Go 1.24.3, and it unpacks that tarball (as
   root) only after its sha256 has matched the one go.dev publishes for the file
   (`3333f6ea53afa971e9078895eaa4ac7204a8c6b5c68c10e6bc9a33e8e391bdd8`, pinned in
   the script). (`TestDeployGoToolchainIsCheckedBeforeItIsUnpacked`) It builds as
   root from your checkout, in `/var/cache/nhbchain-build` (only root can write
   it), and runs its own tools from there, never from `/opt/nhbchain`, which
   belongs to the service user (see
   [below](#what-a-snapshot-host-cannot-make-the-script-do)).
3. **When it will install a snapshot** (the data directory holds nothing, or
   `--reset-state` was given), **fetches only the manifest first** and checks that
   it is for the pinned chain id and genesis hash (and for the tip hash and state
   root you pinned, if you did), that the commit it names is the release you
   pinned with `--min-release-commit` or built on it (if you pinned one), and that
   the node built here is the one the snapshot was taken with. When it is not, it
   says which commit to check out, and to check first that it is a release you
   recognise: the manifest is not signed, and the host that serves it chooses that
   commit. It reads this checkout's own commit with git told that this checkout is
   safe to read, for that one command (a checkout that another user owns, run as
   root, is otherwise refused by git as "dubious ownership"), and when git still
   cannot read it, it shows git's message instead of sending you to check out the
   commit you are already on. A node that already holds the chain gets none of this
   (see [Running it again](#running-it-again-and---reset-state)).
   (`TestDeployInstallsAVerifiedSnapshotAndRefusesTheRest`,
   `TestDeployBinaryIdentityPolicy`, `TestDeployPinsTheSnapshotsTipAndStateRoot`,
   `TestDeployMinimumReleaseCommit`, `TestDeployTheLocalCommitIsReadOrExplained`)
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
   changed, and **waits until the node is at the network tip**. The node's RPC has
   `--rpc-timeout` (180 seconds) to answer for the first time; a service that is
   not running, or that crash-loops, never does, and the script then stops with
   that message and the commands that show why (`systemctl status`, `journalctl`).
   It checks on the first answer that the node reports the pinned chain id and
   genesis hash. At the tip means all of these, on two polls in a row:
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

   It stops with a diagnosis when the RPC does not answer within 180 seconds,
   when the node makes no progress (or answers nothing) for 15 minutes, or when
   the timeout passes.
   (`TestDeployWaitUntilSynced`, `TestDeployWaitUntilSyncedNeedsBlocksPastTheSnapshot`,
   `TestWaitSynced*`, `TestAForgedSnapshotIsRefusedAndAnUnconnectedNodeOnItIsNeverAtTheTip`)
8. **Only then** submits the reward beneficiary and the zero-value registration
   and prints the next steps.

What the unpacking checks (all in `nhb-snapshot extract`; every one has a test
in `cmd/nhb-snapshot/snapshot_test.go`):

- the archive's size and sha256 against the manifest, before anything is written;
- the manifest against the chain id and genesis hash you pin (and, if you give
  them, a minimum height, a maximum age, a tip hash and a state root). The
  maximum age is the age of the snapshot's newest block, and `extract` checks it
  again on the block time of the database it unpacked, so a manifest that claims
  to have been made a minute ago does not get an old snapshot past it
  (`TestMaxAgeIsNotDefeatedByAManifestThatLiesAboutWhenItWasMade`);
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
- **An interrupted first run is finished by the next one, with the same guard.**
  The run that installs a snapshot records its height in
  `/var/lib/nhbchain/.snapshot-height` and removes that record only when it has
  seen the node at the tip, past the snapshot's height. If the run is interrupted
  before that (an SSH session that dropped, a Ctrl-C), the next run finds the data
  in place, fetches no manifest, and reads the height from the record, so it still
  requires the node to apply a block above it. Without the record a re-run would
  have no height to require, and "at the tip" would rest on a peer and a block
  dated close to this host's clock. A record that does not hold a block height
  stops the run and says why. (`TestDeployRemembersTheSnapshotHeightUntilTheNodeHasBeenSeenPastIt`)
- Two runs at the same time are refused. The lock is a directory in the state
  directory of the user who runs the script (`$XDG_STATE_HOME/nhbchain`, or
  `~/.local/state/nhbchain`), which only that user can write: it used to be a fixed
  name in `/tmp`, which any user could make first (stopping every run), and whose
  holder, a process of another user, could not be signalled and so looked gone.
  (`TestDeployLock`)
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
- A host that sends too slowly is given up on: the transfer is aborted when it
  runs below 10 KiB a second for a minute (with the three retries, a few minutes
  at most), where `--max-time` alone, which is two hours, would let a host that
  trickles bytes hold even the manifest, a few kilobytes, for hours.
  (`TestDeployFetchGivesUpOnAHostThatTrickles`)
- Root does not write, chown, chmod or copy through a path the service user can
  replace. The data directory, the key file, the download directory and the
  fingerprint of the running service are all in directories the service user owns,
  so it can put a link in place of any of them. The script therefore does its work
  in them as that user (unpacking, closing the data directory to other users,
  keeping the fingerprint, carrying the node's identity and vote state over on
  `--reset-state`), and where root has to act on the key file it refuses a link and
  sets the ownership on the name (`chown -h`), never on what a link leads to.
  `restore_identity`, which copies the identity and vote files of the old data
  directory, refuses a link as a source (and a `p2p` directory that is one), and
  `write_env` refuses a `node.env` that is a link before it reads the RPC secret
  from it. A few reads as root remain (a hash of the config and of `node.env`,
  whether the data directory is empty) that disclose nothing.
  (`TestDeployRestoreIdentityRefusesALink`, `TestDeployKeyIsNeverHandledThroughALink`,
  `TestDeployRootTouchesNothingTheServiceUserCanReplace`)
- Root and the operator run, install and build with nothing the service user can
  replace. `/opt/nhbchain`, `/var/lib/nhbchain` and `/etc/nhbchain` are that user's
  (`nhb.service` runs as it, and it is the network-facing part of the host), so
  the script takes nothing from them that it then runs as root or as you. It builds
  from the checkout it runs from, with its Go caches and its own copy of the tools
  in `/var/cache/nhbchain-build`, a directory only root can write (a cache under
  `/opt/nhbchain` could be filled with entries that root's next build compiles into
  what it runs). The tools it runs itself come from there, and their answers
  decide things: `nhb-cli` makes the key the validator has (`generate-key`),
  `nhb-snapshot` says whether a config is installed (`check-config`) and lets the
  registration go ahead (`wait-synced`). The copies in `/opt/nhbchain/bin` are run
  by the service user only. `nhb.service` is installed from the checkout, not from
  the copy in `/opt/nhbchain`, which could have been rewritten in the minutes to
  hours between the build and that step. The script refuses to run from inside one
  of the service user's directories (a second run from `/opt/nhbchain` would run a
  script that user can rewrite). The command the script prints for you to make an
  RPC token reads the node's secret as data (`sed`) and never runs `node.env`,
  which that user can replace. What remains: root still copies the checkout into
  `/opt/nhbchain` and hands it to the service user (`rsync`, `chown -R`), the one
  place where root writes into that user's tree; a service user that is already
  compromised and racing those calls is not excluded by anything here.
  (`TestDeployRunsTheToolsRootBuiltAndNeverTheServiceUsersCopies`,
  `TestDeployBuildsAsRootInADirectoryOnlyRootCanWrite`,
  `TestDeployInstallsTheUnitFromTheCheckoutNotFromTheServiceUsersTree`,
  `TestDeployScriptRunsNothingOfTheInstallDirectoryAsRootOrTheOperator`,
  `TestDeployRefusesToRunFromInsideTheServiceUsersDirectories`,
  `TestDeployTokenRecipeReadsTheNodeSecretAsDataNotAsCode`)
- The short-lived RPC token that submits the registration goes to `nhb-cli` on a
  pipe and into its environment, never on a command line: `sudo -u nhb env
  NHB_RPC_TOKEN=... nhb-cli` stays in the process list, where any user can read it,
  for as long as sudo waits. The commands the script prints for you to run later
  pass it through the environment as well (`sudo --preserve-env=...`).
  (`TestValidatorStepsPassTheLocalRPCToken` in `tests/scripts`)

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
- **The commit the manifest names is the host's choice.** It is the commit the
  script tells you to build, and a host that names an old commit steers you to old
  consensus code. Check that it is a release you recognise before you check it out,
  and pass the oldest release you accept as `--min-release-commit`: the script then
  refuses a commit that is not that release or built on it.

## Making snapshots

`scripts/make-snapshot.sh` runs on a host that runs a validator or a follower, **as
the user the node runs as** (the owner of its data directory). It only reads the
node's data directory. It never stops, signals, locks or writes to the node.

```bash
sudo -u nhb bash scripts/make-snapshot.sh \
  --data-dir /var/lib/nhbchain/nhb-data \
  --out-dir /var/lib/nhbchain/snapshots
```

**Why not as root.** The node is the network-facing part of the host, and its user
owns its data directory, so whatever that user puts there is an input of this
script, and what the script copies is published. Run as root, it would read
whatever the user pointed a link at (a link called `999999.log`, aimed at a file
only root can read) and publish it. The script therefore refuses to run as root
against a directory that root does not own, or that root owns but its group or
others can write (`root:nhb` with mode `0775` lets the node's user put a link in it
just the same), and says how to run it (`sudo -u <the directory's owner> bash ...`).
Run as the directory's owner, nothing it reads is more than that user already
holds. If the node itself runs as root, the directory
is root's and the script may be run as root; then `--tool`, `--work-dir` and
`--out-dir` have to be given, and all three have to be places nobody but root can
change (the script checks that each one, and every directory above it, is root's and
cannot be written by its group or by others; a directory that does not exist yet is
made only where the nearest one that does is root's own): as root it executes the
tool and writes the staging area, and it does not take either from a default place
next to the node's data. Run as anyone else, the default tool is used only when the user running the
script, or root, owns it. (`TestMakeSnapshotAsRoot`, `TestRootOnlyPath`,
`TestMakeSnapshotDefaultToolIsNotTakenFromAnotherUser`)

It writes into `--out-dir`, in this order, an archive
`nhb-snapshot-<genesis prefix>-h<height>.tar.gz`, its manifest, and the same
manifest as `manifest.json` (the fixed name a new node fetches). Uploading them is
a separate step and is your decision: where snapshots are hosted is not part of
this repository. The node's user writes `--out-dir`, so whoever copies it on as
another user must copy regular files only and never follow a link. Old snapshots
stay where they are: pass `--keep N` to keep only the `N` newest snapshots of each
chain in `--out-dir` after a run and delete the archives and manifests of older
ones (the one just made, and the one `manifest.json` names, are never deleted, and
nothing but files named exactly as this script names them is). Without `--keep`
every snapshot is kept, and each is about as large as the database (334 MB when the
live chain had 214,000 blocks).
(`TestMakeSnapshotKeepPrunesOnlyOldSnapshotsOfTheSameChain`)

**Which build the snapshot names.** The manifest records the node binary the
database was written by (its sha256) and the source commit it was built from. A
new node runs every later block with its own build, so `deployvalidator.sh`
accepts a snapshot only when its build is that binary or that commit, and tells
the operator to check the commit out. The command above is for a host that
`scripts/deployvalidator.sh` installed: the binary is `/opt/nhbchain/bin/nhb`, the
checkout it was built from is `/opt/nhbchain`, and both are read from there. **On
a host that was not installed by `scripts/deployvalidator.sh`** (a checkout in
another place, a binary somewhere else) the script cannot find them, and it stops
before it copies anything rather than publish a manifest that no new node can use.
Pass the path of the binary that is running and the full commit id it was built
from:

```bash
sudo -u nhb bash scripts/make-snapshot.sh \
  --data-dir /path/to/nhb-data --out-dir /path/to/snapshots \
  --node-binary /path/to/nhbchain/bin/nhb \
  --binary-commit "$(git -C /path/to/nhbchain rev-parse HEAD)"
```

- `--binary-commit` is the whole commit id (40 hexadecimal digits, as `git
  rev-parse HEAD` prints it): a new node compares it with its own checkout's, and
  an abbreviation or a tag would never match. It has to be the commit the running
  binary was built from, not a checkout that was updated after the last build, and
  it has to contain this procedure (see [What you need](#what-you-need)).
- git refuses to read a checkout that another user owns. Run as the checkout's
  owner (the service user, in the layout the installer makes) the script reads the
  commit; run as any other user it says what git said and stops: pass
  `--binary-commit`.
- `--allow-unknown-binary` publishes a manifest that does not name the commit
  anyway, for tests. A new node then refuses it, and is told there is nothing to
  check out, unless it passes `--allow-binary-mismatch`
  (`TestMakeSnapshotRefusesToPublishAManifestNoNewNodeCanAccept`).

How the copy is made. The chain database is a LevelDB directory, and the script
copies the files **the database refers to, and only those**, chosen by what the
database says and never by a listing of the directory:

1. `CURRENT`, and the MANIFEST it names, are copied first. They say what the rest
   is: `nhb-snapshot refs` reads the copied MANIFEST and prints the tables it lists
   and the journal it names.
2. Those tables are hard-linked into a private staging directory (copied when a
   hard link is not possible): they never change once written. (`ln -P`, `cp -P`:
   a link is never followed.)
3. The journal the MANIFEST names is copied last, because it changes while the node
   runs.
4. A pass counts only if nothing changed underneath it: the same `CURRENT`, the
   same MANIFEST at the same size, the same set of journals before and after, every
   listed table copied, and the copied MANIFEST exactly as long as it was when the
   pass began. The MANIFEST size is read before the copy, so a table it refers to
   cannot be missing from the list. Passes repeat until two consecutive ones agree.
   (`TestMakeSnapshotRetriesWhenTheDatabaseChangesDuringAPass`,
   `TestMakeSnapshotUnderChurn`)
5. The staged copy is opened read-only by `nhb-snapshot info --staged`, which
   refuses a copy that holds **any file its own MANIFEST does not refer to**, any
   file that is not a regular file, a journal that does not read as a journal of
   write batches (chunk by chunk, checksum by checksum; the only thing it accepts is
   an end cut short, which is what a copy of a file a node was writing can have),
   and a table that does not read in full. It then prints and checks the chain id,
   genesis hash, height, tip hash and state root, and re-hashes the whole state
   trie. A copy that does not pass is never packed.
   (`TestVerifyStageRefusesWhatTheDatabaseDoesNotReferTo`,
   `TestCheckJournalAcceptsEveryPrefixOfARealJournal`,
   `TestCheckJournalRefusesWhatIsNotAJournal`,
   `TestAFileThatIsNotATableIsRefusedEvenWhenTheMANIFESTListsIt`)
6. It is packed deterministically (sorted names, fixed owner, mode and time, no
   gzip name or time; the same files always give the same archive with one build
   of the tool), written with its manifest, unpacked again and opened again, and
   only then published.

What that means for a data directory the node's user can put anything in:

- A file that only has the name of one of the database's (`999999.log` holding a
  page of text, a table nothing lists, an older MANIFEST, a journal the MANIFEST
  does not name) is never copied and never packed, and the script says so
  (`... (the MANIFEST does not refer to it: not copied)`).
  (`TestMakeSnapshotDoesNotPackAFileThatOnlyHasTheNameOfOneOfTheDatabases`)
- A name of the database that is not a regular file (a link, a directory) ends the
  run before anything is read: `refusing to run: 999999.log in ... is a symbolic
  link, not a regular file`. Nothing is published.
  (`TestMakeSnapshotNeverFollowsALinkInTheDataDirectory`,
  `TestMakeSnapshotRefusesWhatFileKindReportsAsALink`,
  `TestMakeSnapshotRefusesANameOfTheDatabaseThatIsNotARegularFile`)
- A work directory or staging directory that is a link is not used.
  (`TestMakeSnapshotRefusesAPlantedWorkDirectory`)

Which files are in a snapshot: `CURRENT`, `MANIFEST-*`, `*.log`, `*.ldb`,
`*.sst`, chosen by name from that allow-list and then by what the MANIFEST refers
to, and nothing else. They hold blocks, the state trie and indexes: data every node
already has and that is public on chain. No key, and nothing that identifies the
node.

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
and `--binary-version`, or from the checkout the binary was built in, as above),
and the archive's name, size, sha256 and the size and sha256 of every file in it.

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
  keeps the snapshot from predating a release. The schedule is yours to set. The
  limit is judged on the date of the snapshot's newest block, the value `extract`
  checks against the database it unpacked; the creation time in the manifest is
  not signed by anyone and is not what the limit rests on.
- If a snapshot is stale, get a newer one: run the script again with
  `--reset-state` and a fresher `--snapshot-url`. Do not start from an empty data
  directory.

## How long the node takes to start

`--rpc-timeout` (default 180 seconds) bounds how long the node's RPC may stay
silent after the service starts, and a node that does not answer in that time is
reported as not running or crash-looping (exit 7 of `wait-synced`). The default was
first chosen without a measurement, so it was measured, on the local network of the
end-to-end test (real `nhb` processes, the shipped `config.toml` with the shipped
consensus timeouts, Windows 11, a local disk, blocks that carry almost no
transactions). The chain was grown on fast timeouts to a height, the validator was
killed the hard way (as a crash leaves a database), and it was started again three
times; each figure is the time from starting the process to its first answer to
`net_info`:

| Database | Size | Three starts in a row | First start on a database without the transaction-index marker |
|---|---|---|---|
| 1,000 blocks | 0.9 MB | 66 to 90 ms | |
| 4,000 blocks | 3.5 MB | 68 to 113 ms | 134 ms |
| 12,000 blocks | 11 MB | 89 to 133 ms | 194 ms |
| 30,000 blocks | 31 MB | 131 to 236 ms | |

The time grows with the height, by a few microseconds a block: the node reads the
height index of the whole chain into memory when it starts, and, once for a
database, walks every block to index its transactions when the database lacks the
marker that says it did (`BackfillTransactionIndex` in `core/blockchain.go`; a
snapshot of a validator that has run this code carries the marker, and a database
that lacks it pays the walk on its first start only, which is the last column).
Extrapolated linearly to 250,000 blocks, a little more than the live chain has, that
is about 1 to 1.5 seconds for an ordinary start and 3 to 5 seconds for a first start
that runs the walk. Ten times that, for a slower disk, blocks that carry transactions and a cold
cache, is 50 seconds at most, under a third of the default. It is not close, so the default
stays at 180 seconds.

What this does not show: it was not measured on a database of the live chain's size
(it was 334 MB at 214,000 blocks), on Linux, or on a host with little memory and
a slow disk, where a start that swaps can take far longer than these figures. A node
that is starting, only slowly, is visible in `journalctl -u nhb.service`; give
`--rpc-timeout` a larger value then. To measure again:

```bash
NHB_SNAPSHOT_E2E_STARTUP=1 NHB_SNAPSHOT_E2E_STARTUP_TARGETS=1000,4000,12000,30000 \
  NHB_TEST_BASH=/usr/bin/bash go test ./cmd/nhb-snapshot -run TestNodeStartupTime -v -timeout 90m
```

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

What the script does, without it (Ubuntu; adjust paths). Each block says which
function of `scripts/deployvalidator.sh` it stands for, where every value and every
mode below comes from. The steps that unpack, and everything that touches the
service user's directories, are run **as that user** (`sudo -u nhb`), as the script
does. Root builds in `/var/cache/nhbchain-build`, a directory only root can write,
and the tools you run yourself or as root (making the key, checking the config,
waiting for the tip) are the ones built there, never the copies in
`/opt/nhbchain`: that tree belongs to the service user, who could have replaced
them.

```bash
# 0. The service user, the directories, the tree and the build
#    (install_tree_and_build). Run it in a checkout of the commit the manifest names,
#    and stay in it: root builds from the checkout, with its caches and its own copy
#    of the tools in /var/cache/nhbchain-build, and /opt/nhbchain gets copies.
SRC=$PWD; B=/var/cache/nhbchain-build
sudo useradd --system --home /opt/nhbchain --shell /usr/sbin/nologin nhb
sudo install -d -m 0700 -o nhb -g nhb /etc/nhbchain
sudo install -d -o nhb -g nhb /var/lib/nhbchain
sudo mkdir -p /opt/nhbchain
sudo rsync -a --delete ./ /opt/nhbchain/
echo "10932798a0058ae35b135dae1a6ee1bdf6a8bc528a55c1eeb3e9eaab534f4b3b  /opt/nhbchain/config/genesis.relaunch.json" | sha256sum -c
sudo install -d -m 0755 -o root -g root $B $B/bin
sudo install -d -m 0700 -o root -g root $B/go-cache $B/go-path $B/go-tmp
for p in nhb nhb-cli nhb-snapshot; do
  sudo env PATH=/usr/local/go/bin:/usr/bin:/bin GOCACHE=$B/go-cache GOPATH=$B/go-path \
    GOTMPDIR=$B/go-tmp TMPDIR=$B/go-tmp HOME=/root \
    go build -trimpath -ldflags="-s -w" -buildvcs=false -o $B/bin/$p ./cmd/$p
done
sudo mkdir -p /opt/nhbchain/bin
sudo install -m 0755 $B/bin/nhb $B/bin/nhb-cli $B/bin/nhb-snapshot /opt/nhbchain/bin/
sudo chown -R nhb:nhb /opt/nhbchain

# 1. This validator's key, made on this machine and never passed in (ensure_key),
#    with the tool root built.
( cd "$(mktemp -d)" && $B/bin/nhb-cli generate-key >/dev/null && \
  sudo install -m 0600 -o nhb -g nhb wallet.key /etc/nhbchain/validator.key )

# 2. Fetch the manifest, then the archive it names (https), as the service user,
#    into a directory only that user can enter (fetch_manifest, install_snapshot).
D=/var/lib/nhbchain/.snapshot-download
sudo -u nhb sh -c "mkdir -p $D && chmod 0700 $D"
sudo -u nhb curl -fsSL -o $D/manifest.json https://SNAPSHOT-HOST.example/PATH/manifest.json
ARCHIVE=$(sudo -u nhb /opt/nhbchain/bin/nhb-snapshot manifest show --manifest $D/manifest.json --field archive.name)
sudo -u nhb curl -fsSL -o "$D/$ARCHIVE" "https://SNAPSHOT-HOST.example/PATH/$ARCHIVE"

# 3. Verify against the pinned identity, then unpack, as the service user.
PIN="--chain-id 18346390202490284624 --genesis-hash 0xfe9b78af9223ea50f456f63c41084dd99bac4aaa3a790a10fcc26d1dc63210a2"
sudo -u nhb /opt/nhbchain/bin/nhb-snapshot verify --manifest $D/manifest.json --archive "$D/$ARCHIVE" $PIN --max-age 72h
sudo -u nhb /opt/nhbchain/bin/nhb-snapshot extract --manifest $D/manifest.json --archive "$D/$ARCHIVE" $PIN --max-age 72h \
  --target /var/lib/nhbchain/nhb-data \
  --reject-validator "$(sudo -u nhb /opt/nhbchain/bin/nhb-cli address /etc/nhbchain/validator.key | grep -o 'nhb1[a-z0-9]*')"
sudo -u nhb chmod 0700 /var/lib/nhbchain/nhb-data

# 4. Config: copy config.toml and change only the node-local keys, then check it
#    (install_config; render_config does the editing with a small perl program).
#    Set ListenAddress, RPCAddress, DataDir = "/var/lib/nhbchain/nhb-data",
#    GenesisFile = "/opt/nhbchain/config/genesis.relaunch.json",
#    ValidatorKeystorePath = "", ValidatorKMSEnv = "NHB_VALIDATOR_RAW_KEY",
#    NetworkName = "nhb-mainnet-validator", and in [p2p] NetworkId = 18346390202490284624,
#    Bootnodes = ["BOOTNODE-HOST.example:6001"], PersistentPeers = the same, and
#    ExternalAddress = "<this server's public IP>:6001".
sudo install -m 0600 -o nhb -g nhb /opt/nhbchain/config.toml /etc/nhbchain/config.toml
sudo -u nhb nano /etc/nhbchain/config.toml
sudo $B/bin/nhb-snapshot check-config --config /etc/nhbchain/config.toml --genesis /opt/nhbchain/config/genesis.relaunch.json

# 5. The node's environment: the RPC secret and the key, readable by root only
#    (write_env; systemd reads it and hands it to the node).
sudo sh -c 'umask 077; { echo NHB_ENV=prod; echo "NHB_RPC_JWT_SECRET=$(openssl rand -hex 32)"; echo "NHB_VALIDATOR_RAW_KEY=$(od -An -tx1 /etc/nhbchain/validator.key | tr -d " \n")"; } > /etc/nhbchain/node.env'

# 6. The service (install_service), from the checkout and not from /opt/nhbchain,
#    then start the node and wait for the tip.
sudo install -m 0644 $SRC/deploy/systemd/nhb.service /etc/systemd/system/nhb.service
sudo systemctl daemon-reload
sudo systemctl enable nhb.service
sudo systemctl start nhb.service
$B/bin/nhb-snapshot wait-synced --rpc http://127.0.0.1:8545 $PIN \
  --min-height "$(sudo -u nhb /opt/nhbchain/bin/nhb-snapshot manifest show --manifest $D/manifest.json --field height)" \
  --tip-rpc https://TRUSTED-RPC-HOST.example

# 7. Only then register (submit_validator_steps): a short-lived token, which goes
#    through the environment and never on a command line.
TOKEN=$(sudo sed -n 's/^NHB_RPC_JWT_SECRET=//p' /etc/nhbchain/node.env | sudo -u nhb /opt/nhbchain/bin/nhb-cli rpc-token --secret-stdin)
NHB_RPC_TOKEN="$TOKEN" RPC_URL=http://127.0.0.1:8545 sudo --preserve-env=NHB_RPC_TOKEN,RPC_URL -u nhb \
  /opt/nhbchain/bin/nhb-cli set-reward-beneficiary nhb1YOUR_WALLET /etc/nhbchain/validator.key
NHB_RPC_TOKEN="$TOKEN" RPC_URL=http://127.0.0.1:8545 sudo --preserve-env=NHB_RPC_TOKEN,RPC_URL -u nhb \
  /opt/nhbchain/bin/nhb-cli register-validator 0 /etc/nhbchain/validator.key
```

This is the same order the script keeps: nothing is registered before the node has
been seen past its snapshot's height. What the script adds is the checks around it
(the size limits, the binary and release checks, refusing a key that already runs
elsewhere or a second node on the host, the record that makes an interrupted run
finish with the same guard) and the exact stop-and-swap of `--reset-state`.

## Troubleshooting

| You see | It means |
|---|---|
| `the snapshot is for chain ... not the pinned network` | The manifest is for another network. Nothing was installed. |
| `the node built here is not the one the snapshot was taken with` | Your build differs from the snapshot's. `git checkout` the commit it prints and run the script again. `--allow-binary-mismatch` is only for two builds that differ in nothing consensus executes. |
| `The snapshot does not say which commit it was made with` | The manifest's commit is `unknown`, so there is nothing to check out. Ask whoever published it for a snapshot made by a current `make-snapshot.sh`, which refuses to make one that does not name its commit. |
| `the source commit of the node binary is not known` and `a dead end for every new node` (from `make-snapshot.sh`) | The producer could not read the commit from the checkout above the node binary (a host not installed by the script, or git refusing a checkout another user owns). Pass `--node-binary` and `--binary-commit`; see [Making snapshots](#making-snapshots). Nothing was copied or published. |
| `the commit the snapshot's manifest names (...) is neither the release you pinned (...) nor built on it` | `--min-release-commit` was given and the manifest's commit is older than that release, or on another branch. The manifest is not signed; do not use a snapshot that sends you there. |
| `the commit the manifest names (...) is not in this checkout` (or `the release you pinned (...) is not in this checkout`) | The script cannot compare two commits when one is missing. Run `git fetch` in the checkout; if the manifest's commit is still missing it is not part of the project's history. |
| `This checkout's commit could not be read: git said: ...` | git refused the checkout (typically `detected dubious ownership`, when it belongs to another user) or is not installed. The script names the checkout as safe for its own git calls; if git still refuses, fix what it says (`git config --global --add safe.directory <the checkout>`), or check the commit out from a git that can read it. |
| `the Go 1.24.3 tarball has sha256 ..., not the ... go.dev publishes` | A damaged or replaced download of the toolchain. It was not unpacked. Run the script again; if it happens again, do not go on. |
| `could not download ...` and `the host has to send at least 10240 bytes a second` | The host was too slow (under 10 KiB a second for a minute) and was given up on, after the retries. Try again later or use another snapshot host. |
| `an earlier run installed a snapshot at height N and did not finish waiting for the node` | Not an error: the run that installed the snapshot was interrupted before it saw the node at the tip, so this run requires the node to get past height N as well. `/var/lib/nhbchain/.snapshot-height` holds N. |
| `... does not hold a block height` (naming `.snapshot-height`) | That record was changed by hand or damaged. Remove it only if you know the node is at the network tip, or run again with `--reset-state`. |
| `another run of this script (pid ...) is in progress` | The lock in `~/.local/state/nhbchain/deploy.lock.d` (or `$XDG_STATE_HOME/nhbchain`) is held by a live process of yours. A lock left by a process that is gone is taken over. |
| `<path> is a symbolic link, not a key file` (or `... is a symbolic link, not the file the node wrote`) | Something replaced `/etc/nhbchain/validator.key`, or a file of the node's identity or vote state, by a link. The script never follows one. Find out who made it (the service user owns those directories), and remove it. |
| `refusing to run as root: ... belongs to nhb` (from `make-snapshot.sh`) | Run it as the data directory's owner, as the message says: `sudo -u nhb bash scripts/make-snapshot.sh ...`. |
| `refusing to run: 999999.log in ... is a symbolic link, not a regular file` (from `make-snapshot.sh`) | A name of the chain database in the node's data directory is not a regular file (a link, a directory). The node never makes one. Nothing was copied or published; find out who made it. |
| `... (the MANIFEST does not refer to it: not copied)` (from `make-snapshot.sh`) | A file in the node's data directory has the name of one of the database's but the database does not refer to it (a table a compaction was writing, an older MANIFEST, a leftover, or something else put there). It is not in the snapshot. |
| `refusing to pack ...: the MANIFEST does not list ...` or `... names the journal ..., not ...` (from `nhb-snapshot info --staged` or `pack`) | The staged copy holds a file the database does not refer to. Nothing was packed. |
| `the snapshot's newest block is dated ... ago, older than the allowed ...` | The snapshot is stale for the `--max-snapshot-age` you gave, whatever creation time its manifest states. Get a newer one. |
| `the archive sha256 is ... the manifest says ...` or `a truncated or replaced download` | A damaged or replaced download. Fetch it again. |
| `the archive entry ... is not a chain database file name` and similar | The archive is not a snapshot made by `make-snapshot.sh`. Do not use it. |
| `the unpacked snapshot does not open` | The archive is genuine but the database inside is damaged or incomplete. Ask for another snapshot. |
| `... is a validator in this snapshot's state` | On a first run: the key on this host is already a validator. It must not start a second node. (`--reset-state` on a node's own data directory does not make this check.) |
| `the node's RPC did not come up within ... seconds` (and `waiting for the node's RPC: ... connection refused` before it) | `nhb.service` is not running, or it is crash-looping (`Restart=on-failure` starts it again every few seconds, so `systemctl status` may say `activating`). The node never answered in `--rpc-timeout` (180 seconds). The usual causes are a config it cannot read (owner and mode of `/etc/nhbchain`), a data directory it cannot open, and being killed for lack of memory; `journalctl -u nhb.service -n 80` says which. A very large database on a slow disk may need a longer `--rpc-timeout`. |
| `the node did not reach the network tip within ...` | Still catching up: raise `--sync-timeout`, or the snapshot is too old (see the measured gap above). |
| `the node has stopped making progress` | No new block for 15 minutes while behind. Check `journalctl -u nhb.service`, the bootnode, and that this host's clock is right. |
| `the node has not answered a request for its newest block` | The node's RPC answered, then stopped answering what the wait asks, for 15 minutes. Check `systemctl status nhb.service` and its journal. |
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
  writes with compactions (`TestMakeSnapshotUnderChurn`: bursts of writes with rests
  between them, every snapshot started with a burst; and
  `TestMakeSnapshotRetriesWhenTheDatabaseChangesDuringAPass`, which makes the
  database change in the middle of the first pass, every time), and of the
  deployment script's functions against fake `sudo`, `systemctl` and `ps` and a
  snapshot of a chain with the live genesis.
- What the producer trusts (`producer_trust_test.go`, `dbrefs_test.go`,
  `stage_test.go`): files that only have a database file's name are not packed; a
  journal is read chunk by chunk and every prefix of a real one is accepted, and
  nothing that is not a journal is; a listed table that is not a table is refused;
  a name that is a link or a directory ends the run (with links made where the
  machine can make them, and through the `file_kind` stand-in where it cannot);
  running as root, root-only places and default tools (through stand-ins for who is
  who, `current_uid` and `owner_and_mode`); `--keep`.
- What the deployment script trusts (`deploy_trust_test.go`): the node's identity
  copied without following a link, the key file handled by name and by its owner,
  root touching nothing the service user can replace, a host that trickles, the
  lock, the pinned Go tarball, the local commit, the minimum release commit, and the
  record that makes an interrupted run keep its guard.
- What the deployment script builds, installs and runs as root
  (`deploy_build_trust_test.go`): the unit comes from the checkout; the tools it
  runs itself come from root's build directory, with copies in the service user's
  tree planted to say a config is right, to write the key and to say the node is at
  the tip; the build reads and writes only root's directory and builds the checkout;
  the recipe for a token reads `node.env` as data; and the script refuses to run
  from inside the service user's directories (through a stand-in for `physical_dir`
  where a link cannot be made). The commands the pages give to sign a transaction
  are compared with the ones the script prints (`docs_signing_test.go`).
- The deployment script's `main()` run whole, with only the steps that need a real
  server (packages, the build, the service, the node's RPC) stubbed, against a real
  `nhb-snapshot` and a snapshot host on the loopback interface: a fresh install,
  running it again with the publisher's snapshot replaced or gone, `--reset-state`
  of a node whose own key is a validator, a snapshot that does not check out, a
  failing swap, the size, disk-space and pin refusals, and the sync check
  (`deploy_rerun_test.go`, `deploy_limits_test.go`, `sync_forged_test.go`,
  `snapshot_limits_test.go`).
- The bounds on the wait for the tip (a node that never answers ends the wait
  after `--rpc-timeout`, and one that stops answering after `--stall-timeout`, not
  after `--timeout`: `TestWaitSyncedGivesUpOnARPCThatNeverComesUp`,
  `TestWaitSyncedDoesNotWaitOutTheTimeoutForANodeThatStopsAnswering`,
  `TestDeployWaitUntilSynced`), the age limit on a snapshot whose manifest lies
  about when it was made (`TestMaxAgeIsNotDefeatedByAManifestThatLiesAboutWhenItWasMade`),
  and what the producer refuses to publish
  (`TestMakeSnapshotRefusesToPublishAManifestNoNewNodeCanAccept`).

Not tested here, and the exact commands to run on Linux:

- The system-level steps of `scripts/deployvalidator.sh` (`apt-get`, `useradd`,
  `rsync` to `/opt/nhbchain`, the Go install, the swap file, `systemctl` start and
  restart, `sudo -u nhb`). On a clean Ubuntu 22.04 VM, serve a snapshot directory
  with `python3 -m http.server 8000 --directory SNAPSHOTS` and run
  `bash scripts/deployvalidator.sh --beneficiary nhb1... --snapshot-url http://127.0.0.1:8000 --allow-insecure-http --bootnode BOOTNODE:6001`.
- The tests that need a real symbolic link (Windows needs a privilege to make one, so
  they skip with a message there): `TestPackRefusesASymlinkedFile`, the symlink
  target case of `TestExtractRefusesBadTargets`,
  `TestMakeSnapshotNeverFollowsALinkInTheDataDirectory`,
  `TestMakeSnapshotRefusesAPlantedWorkDirectory`, `TestRefsNeverReadThroughALink/a_real_link`
  and `TestDeployRestoreIdentityRefusesALink/a_real_link`:
  `go test ./cmd/nhb-snapshot -count=1` on Linux runs them. The same refusals are
  tested everywhere with stand-ins for the question "is this a link?" (`file_kind`
  in `make-snapshot.sh`, `test -L` in `deployvalidator.sh`, the `lstat` of the tool).
- What only real privilege and a real kernel show, which nothing here ran: that
  `cp -P` and `ln -P` copy a link as a link (GNU coreutils behaviour, relied on and
  not observed), that `sudo` keeps the token off its command line when it is given
  on a pipe (`sudo -u nhb sh -c ...`, as the existing secret-on-stdin step does),
  that `sudo --preserve-env` in the printed commands is allowed by the host's
  sudoers (it is by default for a user who may run any command), that git honours
  `-c safe.directory=` on the command line (git 2.35.2 and later) and that a
  checkout another user owns is then read, `chown -h` and `install` replacing a
  link, and `root_only_path` on a real root-owned tree (it is tested against stand-ins
  for `stat`). On a Linux host: run `sudo bash scripts/make-snapshot.sh` against a
  node's directory (it must refuse), then as the node's user with a link named
  `999999.log` aimed at a root-only file in that directory (it must refuse, and
  publish nothing).
- The build and the installation of the tree, which the tests record and do not run
  (root is stubbed): that `go build` in the operator's checkout, with its caches in
  `/var/cache/nhbchain-build`, makes the same binary as a build in `/opt/nhbchain`
  (`-trimpath` and `-buildvcs=false` leave no path in it; the two were not
  compared), that `install -d -m 0700 -o root -g root` sets the mode and the owner of
  a directory that already exists (GNU coreutils behaviour, relied on for a second
  run), that `rsync -a --delete` without the cache excludes removes what an earlier
  version left in `/opt/nhbchain` (some gigabytes: the first run after an upgrade
  takes the time that takes), and that the command that makes an RPC token works
  under a real sudoers policy (the test runs it with a fake sudo and a stand-in
  CLI). On a Linux host: run the script twice, then check that `ls -ld
  /var/cache/nhbchain-build /var/cache/nhbchain-build/*` shows root as the owner
  (modes 755 and 700), that `ls -a /opt/nhbchain` shows no `.gocache`, and that
  `sha256sum /var/cache/nhbchain-build/bin/nhb /opt/nhbchain/bin/nhb` prints the
  same hash twice.
- The producer on ext4 or xfs (hard links were exercised on NTFS):
  `bash scripts/make-snapshot.sh --data-dir DIR --out-dir OUT` against a running node.
- git's refusal of a checkout that another user owns (`detected dubious ownership`,
  Linux only): as root against `/opt/nhbchain` after the installer gave it to the
  service user, `make-snapshot.sh` must stop with git's message and ask for
  `--binary-commit`, not publish `commit=unknown`. The tests cover the same path
  with a checkout git cannot read for another reason (`git could not read`).
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
nhb-snapshot info         --data-dir DIR [--format text|json] [--header-window N] [--no-state-check] [--staged]
nhb-snapshot refs         --data-dir DIR
nhb-snapshot pack         --data-dir DIR --out-dir DIR [--binary-version S] [--binary-commit S] [--binary-sha256 HEX] [--latest]
nhb-snapshot verify       --manifest FILE --archive FILE --chain-id N --genesis-hash HEX [--min-height N] [--max-age D] [--tip-hash HEX] [--state-root HEX] [--max-bytes N]
nhb-snapshot extract      --manifest FILE --archive FILE --target DIR --chain-id N --genesis-hash HEX [--reject-validator ADDR] [--max-bytes N] [--tip-hash HEX] [--state-root HEX]
nhb-snapshot manifest show --manifest FILE [--field NAME]
nhb-snapshot check-config --config FILE --genesis FILE
nhb-snapshot wait-synced  --rpc URL [--tip-rpc URL] [--min-height N] [--max-lag-blocks N] [--max-lag-seconds N] [--timeout D] [--rpc-timeout D] [--stall-timeout D]
```

`refs` prints the files the MANIFEST of a database refers to (`manifest NAME`,
`journal NAME`, `prev-journal NAME` when it names a second one, and `table NAME`
for each table it lists); only `CURRENT` and the MANIFEST have to be in the
directory, which is how `make-snapshot.sh` asks what to copy after it has copied
those two. `info --staged` is `info` for a staged copy: it refuses a directory that
holds any file its own MANIFEST does not refer to, a file that is not a regular
file, a journal that does not read as one, or a table that does not read in full.
`pack` makes the same checks.

`--chain-id` and `--genesis-hash` may come from `NHB_SNAPSHOT_CHAIN_ID` and
`NHB_SNAPSHOT_GENESIS_HASH`; the tool has no default for either. It only ever opens
a database read-only, and only a copy: a data directory that a running node holds
open cannot be read (its lock is taken), which is why a snapshot is made from a copy.
`wait-synced` exits 0 when the node is at the tip (see step 7 above: it has a peer;
with `--min-height` it has applied a block above that height; with `--tip-rpc` it is
within `--max-lag-blocks`, at most 15, of that node and holds the same newest
blocks), 3 on timeout, 4 when the node stopped advancing (or, after its RPC
answered, answers nothing for `--stall-timeout`), 5 when the node or the reference
RPC is on another chain, 6 when the node's blocks differ from the reference node's,
and 7 when the node's RPC never answered within `--rpc-timeout` (default 3
minutes: a service that is not running, or that crash-loops, so that the wait does
not run for the whole `--timeout`). Without `--tip-rpc` it compares the date of the newest block with
this host's clock (`--max-lag-seconds`, default 60, either side), so keep the clock
synchronised. `verify` and `extract` refuse a manifest announcing more than
`--max-bytes` uncompressed bytes (default 64 GiB; the script passes its own, lower,
bound).
