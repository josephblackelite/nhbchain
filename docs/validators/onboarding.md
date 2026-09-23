# Validator Onboarding Guide

This is a literal, cold-start walkthrough for standing up a brand-new NHBChain
validator on a fresh EC2 instance you have never touched before. It assumes
nothing except a running Ubuntu server with SSH access and a terminal in
front of you. Follow it top to bottom in order.

**This network cannot be joined by syncing from genesis.** A new node starts from
a verified snapshot of the chain database and only then registers; the procedure
is in [Onboarding a validator from a snapshot](snapshot-onboarding.md), and Step 1
below is its short form.

If you just want the one-liner and already know what you're doing, see
["Join As A Validator In One Command"](../../README.md#join-as-a-validator-in-one-command)
in the repo README. This document exists for everything that command doesn't
tell you: what it actually does, how to get paid once it's running, how to
check whether your validator is really active, and what to do when something
goes wrong.

## Pre-flight checklist

Do these four things *before* you run anything:

1. **Server sizing.** Use at least a `t3.medium` (2 vCPU, 4GB RAM) with
   enough free disk for the Go module cache — 90GB+ of headroom is a safe
   recommendation. Smaller instances (e.g. `t3.micro`-class, ~908MB RAM, no
   swap) **will** get OOM-killed while compiling this dependency tree, even
   with plenty of free disk space. See "Troubleshooting" below if you're
   stuck on a small box.
2. **Firewall / security group**, opened *before* you start:
   - `22/tcp` — SSH, your own access.
   - `6001` **TCP and UDP** — P2P. Both protocols are required; UDP is the
     one people forget.
   - `8545/tcp` is **optional**. The bootstrap script binds RPC to
     `127.0.0.1` by default (not externally reachable). Only open this if
     you deliberately want direct external RPC/MetaMask access — leaving it
     internal-only is the safer default.
3. **At least `10,000 ZNHB` of stake behind this validator's OWN node
   address**, which you learn at the end of Step 1. Validator eligibility
   rests on that address's total stake: ZNHB **delegated to it from any
   other wallet counts**, and so does ZNHB it stakes itself (see "Staking"
   under Step 2 below; the rule is `validatorEligibilityBasis` in
   `core/state_transition.go`). Either route is enough. If you delegate,
   you do it from your own wallet once you have the address. If you would
   rather self-stake, you send the ZNHB to the server's own address and
   stake it directly on the server; that is *not* a portal delegation.
4. **Understand gas vs. stake before you start** — this is the single most
   common point of confusion:
   - **ZNHB for staking** has to end up as stake on *this validator's node
     address*: delegated to it from any wallet, or sent to it and
     self-staked on the server with the validator's own key. Both count
     toward the same minimum.
   - **NHB for gas** is *not* required to run this validator's heartbeat or
     to set its reward beneficiary — see "Getting paid" below for why.

   Don't send NHB to the server's validator key expecting it to be needed
   for either of those operations; it currently isn't. ZNHB is what counts
   as stake; item 3 above says how it gets there.

## Step 1 — Run the bootstrap script

**This network cannot be joined by syncing from genesis.** The script installs
the node, puts a verified snapshot of the chain database in its data directory,
starts the node as a follower that does not vote, waits until it is at the
network tip, and only then registers the validator. The whole procedure, what it
verifies, how snapshots are made and how old one may be is in
[Onboarding a validator from a snapshot](snapshot-onboarding.md). Read it first.

You need two things from whoever operates the network, neither of which is built
into the script: the location of a snapshot (`--snapshot-url`) and a bootnode
(`--bootnode`). The snapshot's manifest names the source commit the node has to
be built from. On a clean machine `nhb-snapshot` does not exist yet, so read it
from the manifest, which is JSON, with `curl` (the field is
`producer.binaryCommit`):

```bash
curl -fsS https://SNAPSHOT-HOST.example/PATH/manifest.json | sed -n 's/.*"binaryCommit": *"\([0-9a-f]*\)".*/\1/p'
```

**Check that this commit is a release you recognise before you check it out.**
The manifest is not signed: whoever hosts it chooses the commit, and an old
commit is old consensus code. Compare it with what the people who run the network
announce, and pass the oldest release you accept as `--min-release-commit`, so
that the script refuses a manifest that names anything older or on another
branch. Then check the commit out and run. (The script you run is the one in that
commit, so the commit has to contain this procedure,
`scripts/deployvalidator.sh` as described here and `cmd/nhb-snapshot`: a snapshot
made by a validator that still runs an earlier build names a commit whose script
syncs from genesis, which does not work on this network. See
[What you need](snapshot-onboarding.md#what-you-need).)

```bash
bash scripts/validator-only-bootstrap.sh \
  --beneficiary nhb1youroperatorwalletaddresshere \
  --snapshot-url https://SNAPSHOT-HOST.example/PATH \
  --bootnode BOOTNODE-HOST.example:6001 \
  --max-snapshot-age 72h \
  --min-release-commit FULL_COMMIT_ID_OF_THE_OLDEST_RELEASE_YOU_ACCEPT
```

`--max-snapshot-age 72h` refuses a snapshot whose newest block is older than three
days: a follower has to execute every block after its snapshot, and an old one is
the usual cause of a catch-up that never finishes. `--min-release-commit` needs the
full history (a plain `git clone`, not `--depth 1`).

`scripts/deployvalidator.sh` is the same script under the hood --
`validator-only-bootstrap.sh` is a 5-line wrapper around it, and both accept
identical flags. Every step stops the script with an error when it fails, and
it is safe to run again: what is already in place is left alone, and a node that
is already running is restarted only if its binary, config or key changed. A node
that already holds the chain is not given a snapshot, so a run that installs none
does not fetch a manifest and does not depend on the snapshot host at all.

There is no `--reset-state` shortcut for a first run any more. On a fresh machine
the script installs a snapshot by itself; `--reset-state` now means "this node's
data directory is broken": it downloads, verifies and unpacks a snapshot next to
the old data directory first, and only then stops the node, moves the old data
directory aside (it is never deleted) and puts the snapshot in its place, keeping
the node's p2p identity and vote state. It works on a node that is already a
validator; a snapshot that does not check out changes nothing.

### Flags

| Flag | Default | Notes |
|---|---|---|
| `--beneficiary` | *(required)* | Wallet address to redirect the consensus reward to. The script exits with an error if this is omitted. |
| `--snapshot-url` | *(required, or `NHB_SNAPSHOT_URL`)* | Directory URL that holds `manifest.json` and the archive it names. `https` only. |
| `--bootnode` | *(required, or `NHB_BOOTNODE`)* | Plain `host:port`, never an `enode://` URI (see below). |
| `--min-release-commit` | *(none, or `NHB_MIN_RELEASE_COMMIT`; advised)* | The full commit id of the oldest release you accept. The script refuses a manifest whose commit is neither that commit nor a descendant of it, so a snapshot host cannot steer you to old consensus code. Needs both commits in your checkout (`git fetch` first). |
| `--tip-rpc` | *(none, or `NHB_TIP_RPC_URL`)* | RPC URL of a node you trust. Used to decide when the node has caught up, and to refuse a node whose newest blocks are not that node's. |
| `--max-lag-blocks` | `3` | How far from that node (either side) still counts as caught up; at most 15. |
| `--sync-timeout` | `7200` | Seconds to wait for the catch-up. |
| `--rpc-timeout` | `180` | Seconds the node's RPC may stay silent after the service starts. A node that never answers is not running or is crash-looping: the script stops after this long with the commands that show why, and does not wait out `--sync-timeout`. |
| `--max-snapshot-age` | *(any age; use `72h`)* | Refuse a snapshot whose newest block is older than this, for example `72h`. The limit rests on the date of the newest block, which is checked against the unpacked database; the creation time the manifest states is not signed by anyone and cannot get a stale snapshot past it. Without it a snapshot of any age is accepted and a stale one fails only later, with a stall. |
| `--tip-hash`, `--state-root` | *(none)* | The tip hash and state root the snapshot must have (32 bytes of hex), as read from nodes you trust. A snapshot with another tip is refused before it is downloaded. |
| `--max-snapshot-gib` | `16` | Refuse a snapshot whose archive, or whose unpacked database, is larger than this many GiB, or that the disk has no room for. |
| `--listen-addr` | `0.0.0.0:6001` | P2P listen address. |
| `--rpc-addr` | `127.0.0.1:8545` | Not exposed externally by default. |
| `--external-address` | *(auto-detected)* | Falls back to EC2 IMDSv2, then `https://ifconfig.me`, if omitted. |
| `--reset-state` | *(off)* | Replace the data directory with a snapshot: unpack it first, then stop the node, move the old directory aside and put the new one in its place. |
| `--allow-insecure-http` | *(off)* | Accept an `http://` or `file://` snapshot URL. |
| `--allow-existing-key` | *(off)* | Use a validator key that was already on this host. Only if that key runs nowhere else. |
| `--allow-binary-mismatch` | *(off)* | Continue although this build is not the snapshot's. |
| `--help` | | Prints usage. |

The old `--email`, `--onboarding-email-endpoint` and `--network-id` flags are
gone (the network is pinned to chain id `18346390202490284624`), and there is no
default bootnode.

**Bootnode format warning:** the bootnode value must be plain `host:port`
(for example `BOOTNODE-HOST.example:6001`), *never* an `enode://nodeid@host:port`
URI. This codebase's P2P dialer calls `net.Dial("tcp", addr)` directly and never
parses the `enode://` scheme at all. Using that form means the node never even
attempts to dial its bootnode, and fails with `dial tcp: address enode://...: too
many colons in address`. The script refuses it.

### What the script actually does, in order

1. Validates its arguments before touching the system.
2. Installs Go 1.24.3 if it isn't already present, and `rsync`, `perl`, `curl`.
3. Adds a 4G swap file automatically on low-RAM/no-swap hosts (a real
   `t3.micro`-class box was OOM-killed compiling this dependency tree).
4. Creates the `nhb` system user, `/etc/nhbchain` (owned `nhb:nhb`, mode `700`)
   and `/var/lib/nhbchain`.
5. Rsyncs the repo to `/opt/nhbchain` (which the service user then owns), checks
   `config/genesis.relaunch.json` is byte for byte the live genesis, and builds
   `nhb`, `nhb-cli` and `nhb-snapshot` from your checkout, as root, in
   `/var/cache/nhbchain-build`: a directory only root can write, with disk-backed
   `GOCACHE`/`GOPATH`/`GOTMPDIR` (`/tmp` is often a small RAM-backed tmpfs). The
   builds are copied into `/opt/nhbchain/bin` for the service user. The tools the
   script runs itself (`nhb-cli generate-key`, `nhb-snapshot check-config` and
   `wait-synced`) are the ones in `/var/cache/nhbchain-build/bin`, never the copies
   in `/opt/nhbchain`, which the service user could have replaced. For the same
   reason the script refuses to run from inside `/opt/nhbchain`,
   `/var/lib/nhbchain` or `/etc/nhbchain`: run it from a checkout of your own.
6. When it will install a snapshot (an empty data directory, or `--reset-state`;
   a node that already holds the chain needs none, and then nothing is fetched),
   fetches the snapshot manifest, checks it is for the pinned chain id and genesis
   hash (and the tip you pinned, if you did) and that the node just built is the
   binary the snapshot was taken with.
7. Generates a **fresh** validator key locally at `/etc/nhbchain/validator.key`
   (mode `0600`, owned `nhb:nhb`) the first time it runs. It never accepts a key
   via flag or environment variable, reuses the key on later runs, refuses a key
   file it did not create on a host with no node data, and refuses to run while
   another `nhb` process is running outside `nhb.service`.
8. Downloads the archive (refusing one larger than `--max-snapshot-gib`, or that
   the disk has no room for, before it downloads anything), verifies it, checks
   that the key is not already a validator in the snapshot's state (a first run
   only), and unpacks it into `/var/lib/nhbchain/nhb-data` -- never over data that
   is already there, except through `--reset-state`.
9. Writes `/etc/nhbchain/config.toml` from the repository's `config.toml`,
   changing only `ListenAddress`, `RPCAddress`, `DataDir`, `GenesisFile`,
   `ValidatorKMSEnv=NHB_VALIDATOR_RAW_KEY`, `NetworkName`, `NetworkId`,
   `ExternalAddress`, and `Bootnodes`/`PersistentPeers`, and checks the values
   consensus depends on (treasuries, `QuorumCertActivationHeight`) with
   `nhb-snapshot check-config`.
10. Writes `/etc/nhbchain/node.env` (mode `600`, owned `root:root`; the RPC secret
    of an earlier run is kept), installs `deploy/systemd/nhb.service` from your
    checkout, and starts or restarts it.
11. Waits for the node's RPC (at most `--rpc-timeout`, 180 seconds), checks it
    reports the pinned chain id and genesis hash, and waits until it is at the
    network tip: connected to a peer, past the snapshot's height (when this run
    installed one) and, with `--tip-rpc`, within `--max-lag-blocks` of that node
    and holding the same newest blocks as it. It **hard-fails** with diagnostics
    (`systemctl status`, `journalctl`) if the node's RPC never comes up within
    that time (the service is not running or is crash-looping), stops making
    progress, or is on another chain. (Health checks of the RPC must be a
    **POST**: a bare `GET` always returns 400 even on a healthy node.)
12. Only then, with a short-lived RPC token (`NHB_RPC_TOKEN`) that it makes from
    the node's own secret, runs `nhb-cli set-reward-beneficiary <addr> <validator.key>`
    and `nhb-cli register-validator 0 <validator.key>` as the `nhb` user. If a step
    fails, the script stops and prints the command that makes a token and the exact
    retry commands (the same form as in Step 2 below).
13. Prints the node address and what to do next.

### A note on QuorumCertActivationHeight

`QuorumCertActivationHeight` in `config.toml` controls a block-level
quorum-certificate check on the P2P sync path (NHB-TRIAGE-C1). It does not change
state or any block's validity, and it must be the same on every validator. The
shipped `config.toml` sets it to `0`, which is what both live validators run
with: `cmd/nhb/main.go` only applies a value above zero, and the node's default
is "never verify". (Earlier versions of this guide and of `config.toml` said
`451949`; that is not what the live validators run.) Do not change it on one
validator alone.

## Step 2 — Getting paid

There are two separate things, and they are not the same operation:

### Staking (delegation or self-stake)

Validator eligibility requires **>= 10,000 ZNHB of total stake on this
validator's node address** (`staking.minimumValidatorStake`,
governance-adjustable, currently unchanged from its default). That total is
the account's whole stake, **including the ZNHB other wallets have delegated
to it** (through the portal's Validator Hub -> Delegate flow, or any stake
transaction that names the address) and the ZNHB the address staked itself:
`validatorEligibilityBasis` in `core/state_transition.go` reads the
account's `Stake`, which already includes delegated-in amounts, and there is
no separate self-stake requirement. One more condition: the address must not
itself be delegating its own stake to a different validator.

Staking *yield* is a different question and works the other way round:
`stakeRewardBasis`, the basis for what a stake earns, leaves delegated-in ZNHB
out, so ZNHB you delegate to a validator earns for you, the delegator, not
for the validator. Do not read that function as the eligibility rule.

There are two ways to get the stake there, and either is enough:

**A. Delegate** at least 10,000 ZNHB to this validator's node address (the
`nhb1...` address printed at the end of Step 1) from any wallet, for example
the portal's Validator Hub -> Delegate tab. Nothing has to be done on the
server.

**B. Self-stake**, in two steps that both involve this server's own key:

1. **Send >= 10,000 ZNHB directly to this validator's own node address**
   (the `nhb1...` address printed at the end of Step 1) from wherever you
   actually hold ZNHB — an ordinary transfer, the same as sending to any
   other address. Not a portal delegation.
2. **Self-stake it and register in one transaction**, run on the server
   itself using the validator's own key. The key belongs to the service user, so
   the command runs as that user, and the node's RPC only accepts a signed
   transaction with a short-lived token (`NHB_RPC_TOKEN`): the first line makes
   one from the node's own secret, the second signs and sends the stake. The
   token lasts ten minutes; make a new one for the next command.

   ```bash
   TOKEN=$(sudo sed -n 's/^NHB_RPC_JWT_SECRET=//p' /etc/nhbchain/node.env | sudo -u nhb /opt/nhbchain/bin/nhb-cli rpc-token --secret-stdin)
   NHB_RPC_TOKEN="$TOKEN" RPC_URL=http://127.0.0.1:8545 sudo --preserve-env=NHB_RPC_TOKEN,RPC_URL -u nhb /opt/nhbchain/bin/nhb-cli register-validator 10000000000000000000000 /etc/nhbchain/validator.key
   ```

   (`10000000000000000000000` is exactly 10,000 ZNHB in base units --
   raise it if you want extra headroom against the minimum ever being
   raised by governance later.) The bootstrap script already ran this same
   command once automatically with an amount of `0` once the node reached the network tip,
   purely to flip this validator's on-chain `ValidatorRegistered` flag on
   (that part needs no funds and always succeeds) -- this second call with
   real stake is what actually brings its own stake up to the required
   minimum. It's safe to run again with a larger amount later if you want
   to add more self-stake; it isn't a one-time-only operation.

### Consensus reward beneficiary

Without `--beneficiary`, the consensus reward would accrue to the
validator's own server-only address — the bootstrap script requires
`--beneficiary` up front specifically to avoid that. You can also change
the beneficiary later, directly on the server. The key file belongs to the
service user (mode `0600`, in a directory only that user can enter), so the
command runs as that user, and the node's RPC only accepts it with a short-lived
token (`NHB_RPC_TOKEN`) made from the node's own secret:

```bash
TOKEN=$(sudo sed -n 's/^NHB_RPC_JWT_SECRET=//p' /etc/nhbchain/node.env | sudo -u nhb /opt/nhbchain/bin/nhb-cli rpc-token --secret-stdin)
NHB_RPC_TOKEN="$TOKEN" RPC_URL=http://127.0.0.1:8545 sudo --preserve-env=NHB_RPC_TOKEN,RPC_URL -u nhb /opt/nhbchain/bin/nhb-cli set-reward-beneficiary <your-wallet-address> /etc/nhbchain/validator.key
```

Pass an empty string instead of an address to clear a previously-set
beneficiary.

This command **must** be run using the validator's own local key file
(`/etc/nhbchain/validator.key`) directly on the validator server itself —
it's deliberately a local signed-transaction operation, and the key should
never leave the server. If the bootstrap script's own `--beneficiary` attempt
fails, it stops and prints the command that makes a token and this command, with
your address in it, as the way to run it again (the same two lines as here).

**Do not use the portal's "Reward Payout" tab (Validator Hub) to set a real
server-hosted validator's beneficiary.** That form signs with your logged-in
portal wallet's own key, not your validator server's key, so it cannot
correctly redirect a real, independently-keyed validator's rewards. Use the
`nhb-cli` command above, on the server, instead.

### A note on gas

Contrary to older internal notes you may run across, sending a heartbeat
transaction or a `set-reward-beneficiary` transaction from the validator's
own key does **not** require any NHB balance on that key. Both transaction
types go through the default native-transaction handling path and are not
debited against `BalanceNHB`, unlike an ordinary transfer. You do not need
to pre-fund the validator server's key with NHB gas for either operation.
The only real "bring money" step in this whole process is the ZNHB stake
behind this validator's node address, by delegation or by self-stake,
described above.

## Step 3 — Checking status and eligibility

There is currently no single "is my validator active" command. Here is the
best available combination:

- **Stake / delegation state:**

  ```bash
  nhb-cli balance <your-validator-node-address>
  ```

  Shows Staked / Delegated Validator / Pending Unbonds for that address.
  Query this against the node's own local RPC or the public RPC endpoint.

- **Service health:**

  ```bash
  sudo systemctl status nhb.service
  sudo journalctl -u nhb.service -f
  ```

  Use these to confirm the node is actually running and not crash-looping.

- **Network / block height:** query `nhb_getNetworkStats` on the node's RPC,
  or check the public explorer, to see current block height and active
  validator count.

Once registered (the bootstrap script already did this automatically) and
staked to the minimum (delegated to it or self-staked), your node becomes a
validator **candidate**. It
joins the **active** set only after (1) it is online and synced, and (2) it
has begun submitting heartbeats successfully — at the start of the next
epoch boundary after both conditions are met. The validator-selection epoch
is 100 blocks (`core/epoch/config.go` `DefaultConfig().Length`); do not
confuse this with `EpochLengthBlocks = 120` in `config.toml`'s
`[potso.rewards]` section, which is a separate epoch length for POTSO
reward settlement only. This chain's BFT engine does
not produce blocks on a fixed time interval (block time varies with network
conditions and round timeouts), so this document will not give you a precise
"epoch = X minutes" figure — it would not be verifiable and would likely be
wrong. Watch block height / active validator count via the explorer or
`nhb_getNetworkStats` to estimate when the next epoch boundary will land.

## Troubleshooting

All six of these are fixed in the current script. They're documented here
so that if you hit something similar — on an older checkout, a modified
script, or an unusual host — you can recognize the symptom and know the
cause and fix.

### 1. Build gets OOM-killed (`signal: killed`, or the build process just vanishes)

**Cause:** compiling this dependency tree needs real memory headroom, and a
tiny or no-swap instance runs out.
**Fix:** the current script auto-adds a 4G swapfile on such hosts — if
you're still hitting this, confirm you're on a current script checkout.
Otherwise, manually add swap, or move to a larger instance
(`t3.medium` / 2 vCPU / 4GB RAM is the recommended minimum).

### 2. Build fails with "no space left on device" despite plenty of free disk

**Cause:** `/tmp` is a small RAM-backed tmpfs on many EC2 AMIs, and Go's
build scratch space defaults there.
**Fix:** the current script builds with disk-backed
`GOCACHE`/`GOPATH`/`GOTMPDIR` under `/var/cache/nhbchain-build`. If you're
hitting this, confirm you're on a current script checkout.

### 3. `nhb.service` crash-loops with `panic: Failed to load config: ... permission denied`

**Cause:** `/etc/nhbchain` was created with mode `700` owned by `root`
(typically from a manual `sudo mkdir`) instead of the service user `nhb`.
Since the systemd unit runs as `User=nhb`, it can't even traverse a
root-owned `700` directory.
**Fix:** `chown -R nhb:nhb /etc/nhbchain`. The script does this
automatically — this is only relevant if you hand-rolled part of the setup
yourself.

### 4. Node never comes up / bootnode dial fails with "too many colons in address"

**Cause:** an `enode://` URI was used instead of plain `host:port` for the
bootnode.
**Fix:** always use `host:port` form, e.g. `BOOTNODE-HOST.example:6001`. See the
bootnode format warning in Step 1 above.

### 5. Validator's heartbeat gets permanently stuck (nonce never advances, node looks alive but never becomes eligible)

**Cause:** a resubmitted heartbeat at the same gas price as one still
pending in the mempool gets rejected by replace-by-fee rules, and a
non-block-proposing validator could previously fail to even notice its own
pending heartbeat in order to bump the price. This was a real production
bug. It's now fixed — the node auto-bumps the fee on retry and reads its own
mempool directly.
**Fix:** if you're seeing a stuck nonce on a very old build, that's the
signal to update.

### 6. The bootstrap script prints a false-looking `[OK] Validator node started` even though the service is actually crash-looping

**Cause:** an old version of the script assumed success once the service
unit started, without verifying it.
**Fix:** the current script waits for the node's RPC to answer (a POST of
`net_info`, which also confirms the chain) for at most 3 minutes (`--rpc-timeout`)
and, if it never does, stops with `the node's RPC did not come up within 180
seconds: nhb.service is not running, or it is crash-looping` and the commands that
show why, instead of assuming success or waiting out the sync timeout. If you see
the old banner-only behavior on an old checkout, don't trust it: check
`systemctl status` yourself.

## Common confusions

**"Do I need NHB on the server to run heartbeats or set my beneficiary?"**
No. Neither `TxTypeHeartbeat` nor `TxTypeSetRewardBeneficiary` debits
`BalanceNHB` today — see "A note on gas" above. Don't send NHB to the
server's key expecting it to be required for either.

**"I staked ZNHB, why isn't my validator active yet?"**
Staking it (delegated to the node address, or self-staked on the server via
`register-validator`) makes your validator a *candidate*. It only becomes *active* at the next epoch boundary
(100-block increments -- the validator-selection epoch, not POTSO's separate
120-block reward epoch) after it is online, synced, and successfully
submitting heartbeats. There's no exact wall-clock number to give you
here — watch block height via `nhb_getNetworkStats` or the explorer.

**"I delegated ZNHB to my validator's address through the portal's
Validator Hub, why is it still not active?"**
Corrected 2026-09-09 — this used to say portal delegation "doesn't count."
That is no longer true: by design, delegated-in ZNHB (from any wallet,
including the portal's Validator Hub Delegate flow) counts fully toward
the `staking.minimumValidatorStake` threshold, same as self-stake. If your
delegation already meets the minimum and your validator still isn't
active, the far more likely cause is the *separate* one-time step below —
check that first before moving any more ZNHB around.

**"I delegated enough ZNHB and my node is online/synced, why is it still
not active?"**
Stake *amount* (self-stake or delegated-in, either counts) and the
validator's own on-chain **registration flag** are two different things.
The flag can only ever be set by a self-signed transaction from the
validator's own server key — no third-party delegation transaction can
set it, by design (so a random wallet can never force-register someone
as a validator just by delegating to their address without consent). The
bootstrap script normally flips this automatically via a zero-amount
`register-validator` call once the node is at the network tip (see Step 2) — so if
your node went through that script, this is almost certainly already
done. If it didn't (a manually-migrated or hand-configured node, for
example), run it yourself once:

```bash
TOKEN=$(sudo sed -n 's/^NHB_RPC_JWT_SECRET=//p' /etc/nhbchain/node.env | sudo -u nhb /opt/nhbchain/bin/nhb-cli rpc-token --secret-stdin)
NHB_RPC_TOKEN="$TOKEN" RPC_URL=http://127.0.0.1:8545 sudo --preserve-env=NHB_RPC_TOKEN,RPC_URL -u nhb /opt/nhbchain/bin/nhb-cli register-validator 0 /etc/nhbchain/validator.key
```

This costs no funds and only needs to succeed once. After that, delegate
(or self-stake) up to the minimum through whichever path you prefer, and
your validator becomes active at the next epoch boundary once synced and
heartbeating.

**"Where do I set my reward beneficiary — the portal or the server?"**
The server, using `nhb-cli set-reward-beneficiary` with
`/etc/nhbchain/validator.key` and a short-lived token, as in Step 2. The
portal's "Reward Payout" tab signs with your portal wallet's key, not your
validator server's key, so it can't correctly redirect a real server-hosted
validator's rewards.

## Known discrepancy

You may find internal planning notes (not part of the committed docs) that
refer to a second validator's onboarding as "Phase H" — a normal join flow
using this document, distinct from "Phase E," which refers specifically to
the *primary* validator's own genesis relaunch (2026-08-06). If you're
cross-referencing old internal task names elsewhere, "Phase E" is not this
document's process — it's the primary validator's genesis event.
