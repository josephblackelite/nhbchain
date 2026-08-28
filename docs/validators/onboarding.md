# Validator Onboarding Guide

This is a literal, cold-start walkthrough for standing up a brand-new NHBChain
validator on a fresh EC2 instance you have never touched before. It assumes
nothing except a running Ubuntu server with SSH access and a terminal in
front of you. Follow it top to bottom in order.

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
3. **At least `10,000 ZNHB` ready to send to this validator's OWN node
   address** once you know it (printed at the end of Step 1). Validator
   eligibility is now based on this validator's own self-stake only --
   ZNHB delegated in from a separate wallet does **not** count toward
   eligibility at all (see "Staking" under Step 2 below). You'll send the
   ZNHB to the server's own address and self-stake it directly on the
   server; this is *not* a portal delegation.
4. **Understand gas vs. stake before you start** — this is the single most
   common point of confusion:
   - **ZNHB for staking** needs to end up on *this server's own validator
     key* (send it there once the key exists, then self-stake it) — a
     separate wallet's stake never counts toward this validator's own
     eligibility, no matter how much is delegated.
   - **NHB for gas** is *not* required to run this validator's heartbeat or
     to set its reward beneficiary — see "Getting paid" below for why.

   Don't send NHB to the server's validator key expecting it to be needed
   for either of those operations; it currently isn't. ZNHB, unlike NHB, IS
   needed there now — see item 3 above.

## Step 1 — Run the bootstrap script

On the fresh server, clone the repo (or otherwise get the source onto the
box), then run:

```bash
bash scripts/validator-only-bootstrap.sh \
  --beneficiary nhb1youroperatorwalletaddresshere \
  --email you@example.com \
  --reset-state
```

`scripts/deployvalidator.sh` is the same script under the hood —
`validator-only-bootstrap.sh` is a 5-line wrapper around it, and both accept
identical flags.

**Only pass `--reset-state` the first time**, on a genuinely fresh machine
with no existing chain data. It wipes local chain state and is destructive
if re-run against a node that has already synced or is already validating.
Drop it on any later run.

### Flags

| Flag | Default | Notes |
|---|---|---|
| `--beneficiary` | *(required)* | Wallet address to redirect the consensus reward to. The script exits with an error if this is omitted. |
| `--email` | *(none)* | Best-effort onboarding notification; failure to send is a warning, not fatal. |
| `--onboarding-email-endpoint` | `https://nhbcoin.com/api/v1/validators/onboarding-email` | Where the onboarding email is POSTed. |
| `--bootnode` | `52.1.96.250:6001` | Must be plain `host:port` — see the enode warning below. |
| `--network-id` | `430060579445266314` | Mainnet network ID. |
| `--listen-addr` | `0.0.0.0:6001` | P2P listen address. |
| `--rpc-addr` | `127.0.0.1:8545` | Not exposed externally by default. |
| `--external-address` | *(auto-detected)* | Falls back to EC2 IMDSv2, then `https://ifconfig.me`, if omitted. |
| `--reset-state` | *(off)* | **Destructive.** Wipes local chain state. Fresh starts only. |
| `--help` | | Prints usage. |

**Bootnode format warning:** the bootnode value must be plain `host:port`
(e.g. `52.1.96.250:6001`), *never* an `enode://nodeid@host:port` URI. This
codebase's P2P dialer calls `net.Dial("tcp", addr)` directly and never
parses the `enode://` scheme at all. If you find an `enode://`-style example
in old docs, a stale screenshot, or notes from elsewhere, ignore it — using
that form here means the node never even attempts to dial its bootnode, and
fails with `dial tcp: address enode://...: too many colons in address`.

### What the script actually does, in order

1. Installs Go 1.24.3 if it isn't already present.
2. Adds a 4G swap file automatically on low-RAM/no-swap hosts. This exists
   because a real `t3.micro`-class box (908MB RAM, no swap) was OOM-killed
   compiling this dependency tree, even with plenty of disk free.
3. Creates the `nhb` system user, and creates `/etc/nhbchain` (owned
   `nhb:nhb`, mode `700`) and `/var/lib/nhbchain`. This directory **must**
   be owned by `nhb`, not `root` — see Troubleshooting item 3 for what
   happens if it isn't.
4. Rsyncs the repo to `/opt/nhbchain`.
5. Builds `bin/nhb` and `bin/nhb-cli`, using disk-backed
   `GOCACHE`/`GOPATH`/`GOTMPDIR` under `/opt/nhbchain/.gocache` — because
   `/tmp` is often a small RAM-backed tmpfs on stock EC2 AMIs, and a real
   build filled it and failed with "no space left on device" even with 90GB+
   free on the real disk.
6. Generates a **fresh** validator key locally at `/etc/nhbchain/validator.key`
   (mode `0600`, owned `nhb:nhb`) the first time it runs. It never accepts a
   key via flag or environment variable, and reuses the existing key on
   later runs.
7. Writes `/etc/nhbchain/node.env` (mode `600`, owned `root:root`) with a
   freshly generated `NHB_RPC_JWT_SECRET`.
8. Auto-detects the server's external IP (EC2 IMDSv2 first, then
   `https://ifconfig.me`) if `--external-address` wasn't passed.
9. Patches `config.toml` — `ListenAddress`, `RPCAddress`, `DataDir`,
   `ValidatorKMSEnv=NHB_VALIDATOR_RAW_KEY`, `NetworkId`, `ExternalAddress`,
   and `Bootnodes`/`PersistentPeers`.
10. Installs `deploy/systemd/nhb.service`, runs `daemon-reload`, enables and
    restarts it.
11. Polls the node's own RPC (a **POST** to `nhb_getNetworkStats` — a bare
    `GET` always returns 400 even on a healthy node, so don't try to
    health-check this with `curl -f` on a `GET`) for up to 60 seconds, and
    **hard-fails** with diagnostics (`systemctl status`, `journalctl`) if the
    node never comes up.
12. Runs `nhb-cli set-reward-beneficiary <addr> <validator.key>` as the
    `nhb` user, using the `--beneficiary` address you passed (the script
    already exited earlier if you didn't pass one — see Step 1). If this
    step fails, the script only **warns** and prints the exact retry
    command — it does not fail the script or block the validator from
    starting.
13. If `--email` was given, best-effort POSTs to the onboarding-email
    endpoint. Failure here is also just a warning, never fatal.

## Step 2 — Getting paid

There are two separate things, and they are not the same operation:

### Staking (self-stake, on this server, not a portal delegation)

Validator eligibility requires **>= 10,000 ZNHB of this validator's own
self-stake** (`staking.minimumValidatorStake`, governance-adjustable,
currently unchanged from its default). This is a real, load-bearing
distinction from how staking used to work: ZNHB delegated in from a
*separate* wallet through the portal's Validator Hub -> Delegate flow is
tracked separately and does **not** count toward this validator's own
eligibility at all, no matter the amount. Only stake sitting on the
validator's own key, self-staked directly, counts (see
`core/state_transition.go`'s `stakeRewardBasis` if you want the exact
mechanics).

Two steps, both involving this server's own key:

1. **Send >= 10,000 ZNHB directly to this validator's own node address**
   (the `nhb1...` address printed at the end of Step 1) from wherever you
   actually hold ZNHB — an ordinary transfer, the same as sending to any
   other address. Not a portal delegation.
2. **Self-stake it and register in one transaction**, run on the server
   itself using the validator's own key:

   ```bash
   sudo -u nhb /opt/nhbchain/bin/nhb-cli register-validator 10000000000000000000000 /etc/nhbchain/validator.key
   ```

   (`10000000000000000000000` is exactly 10,000 ZNHB in base units --
   raise it if you want extra headroom against the minimum ever being
   raised by governance later.) The bootstrap script already ran this same
   command once automatically with an amount of `0` right after startup,
   purely to flip this validator's on-chain `ValidatorRegistered` flag on
   (that part needs no funds and always succeeds) -- this second call with
   real stake is what actually brings its own stake up to the required
   minimum. It's safe to run again with a larger amount later if you want
   to add more self-stake; it isn't a one-time-only operation.

### Consensus reward beneficiary

Without `--beneficiary`, the consensus reward would accrue to the
validator's own server-only address — the bootstrap script requires
`--beneficiary` up front specifically to avoid that. You can also change
the beneficiary later, directly on the server:

```bash
nhb-cli set-reward-beneficiary <your-wallet-address> /etc/nhbchain/validator.key
```

Pass an empty string instead of an address to clear a previously-set
beneficiary.

This command **must** be run using the validator's own local key file
(`/etc/nhbchain/validator.key`) directly on the validator server itself —
it's deliberately a local signed-transaction operation, and the key should
never leave the server. This is exactly the retry command the bootstrap
script prints if its automatic `--beneficiary` attempt only warned instead
of succeeding.

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
The only real "bring money" step in this whole process is the ZNHB
self-stake onto this server's own validator key, described above.

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
self-staked to the minimum, your node becomes a validator **candidate**. It
joins the **active** set only after (1) it is online and synced, and (2) it
has begun submitting heartbeats successfully — at the start of the next
epoch boundary after both conditions are met. Epoch length is 120 blocks
(`EpochLengthBlocks = 120` in `config.toml`). This chain's BFT engine does
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
`GOCACHE`/`GOPATH`/`GOTMPDIR` under `/opt/nhbchain`. If you're hitting this,
confirm you're on a current script checkout.

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
**Fix:** always use `host:port` form, e.g. `52.1.96.250:6001`. See the
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
**Fix:** the current script polls real RPC health via a POST to
`nhb_getNetworkStats` and hard-fails with diagnostics on timeout, instead of
assuming success. If you see the old banner-only behavior on an old
checkout, don't trust it — check `systemctl status` yourself.

## Common confusions

**"Do I need NHB on the server to run heartbeats or set my beneficiary?"**
No. Neither `TxTypeHeartbeat` nor `TxTypeSetRewardBeneficiary` debits
`BalanceNHB` today — see "A note on gas" above. Don't send NHB to the
server's key expecting it to be required for either.

**"I staked ZNHB, why isn't my validator active yet?"**
Self-staking (on the server, via `register-validator`) makes your validator
a *candidate*. It only becomes *active* at the next epoch boundary
(120-block increments) after it is online, synced, and successfully
submitting heartbeats. There's no exact wall-clock number to give you
here — watch block height via `nhb_getNetworkStats` or the explorer.

**"I delegated ZNHB to my validator's address through the portal's
Validator Hub, why is it still not active?"**
Because that doesn't count. This is the single most common trap here:
portal delegation and this validator's own eligibility are tracked
completely separately, and only this validator's own self-stake (run
directly on the server, see "Staking" under Step 2) counts toward the
`staking.minimumValidatorStake` threshold — no amount of delegated-in ZNHB
from any other wallet ever does. If you followed older instructions (or
another guide) that told you to delegate through the portal, that ZNHB is
not lost, but it also isn't doing anything for this validator's
eligibility — undelegate it and self-stake it on the server instead, or
send fresh ZNHB directly to the validator's own address and self-stake
that.

**"Where do I set my reward beneficiary — the portal or the server?"**
The server, using `nhb-cli set-reward-beneficiary` with
`/etc/nhbchain/validator.key`. The portal's "Reward Payout" tab signs with
your portal wallet's key, not your validator server's key, so it can't
correctly redirect a real server-hosted validator's rewards.

## Known discrepancy

You may find internal planning notes (not part of the committed docs) that
refer to a second validator's onboarding as "Phase H" — a normal join flow
using this document, distinct from "Phase E," which refers specifically to
the *primary* validator's own genesis relaunch (2026-08-06). If you're
cross-referencing old internal task names elsewhere, "Phase E" is not this
document's process — it's the primary validator's genesis event.
