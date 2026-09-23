# Validator Onboarding Guide

This guide walks through standing up an NHBChain validator on a fresh Ubuntu
server with `scripts/validator-only-bootstrap.sh`, and then explains, from the
code, what makes that node a validator: registration, stake, heartbeats and
epoch selection. Every statement here is taken from the source in this
repository; where behaviour is not defined in code this guide does not describe
it.

**This network cannot be joined by syncing from genesis.** A node started from an
empty data directory does not sync it. A new node starts from a verified snapshot
of the chain database of a node that is already running, syncs the blocks after
it, and only then registers. The procedure, what it verifies, how snapshots are
made and why genesis sync does not work are in
[Onboarding a validator from a snapshot](snapshot-onboarding.md); Step 1 below is
its short form.

The one-command version is in the repo README under
["Join As A Validator In One Command"](../../README.md#join-as-a-validator-in-one-command).

## How a node becomes an active validator

A node's validator key is a secp256k1 key. Its address is the account that
carries the validator's state. Three separate things have to be true, and they
are checked in different places:

1. **Registered.** The account's `ValidatorRegistered` flag is set. The flag is
   set only by a transaction the validator key itself signs: a `TxTypeStake`
   (0x06) whose payload has `registerValidator = true` and no third-party
   validator target (`core/state_transition.go`, `applyStake`; a payload that
   names another validator together with `registerValidator` is refused). No
   other account can set it. `nhb-cli register-validator` builds that
   transaction.
2. **Enough stake, not delegated away.** `core/state_transition.go` `setAccount`
   computes
   `meetsStake := registered && selfDelegated(account, addr) && basis >= minStake`.
   - `basis` is the account's total `Stake`, which includes ZNHB delegated in by
     any other wallet with nothing subtracted (`validatorEligibilityBasis`; its
     doc comment says there is no separate self-stake requirement). Delegating at
     least the minimum to a node address from any wallet therefore counts, and so
     does self-staking, and so does a mix.
   - `selfDelegated` is true when the account is not delegating its own stake
     to a different validator (`DelegatedValidator` empty or equal to itself).
   - `minStake` is the governance parameter `staking.minimumValidatorStake`;
     when unset it is `10000000000000000000000` base units = 10,000 ZNHB
     (`native/governance/types.go` `defaultMinimumValidatorStakeWei`; read by
     `minimumValidatorStake` in `core/state_transition.go`, which accepts a bare
     or a quoted number). `[global.Staking].MinStakeWei` in `config.toml` is only
     format-checked at startup and does not set this threshold (`core/node.go`,
     `ValidateStakingConfig`).
   - Reward accrual is different: `stakeRewardBasis` excludes delegated-in stake,
     so a validator earns staking rewards only on capital it staked itself.
3. **Selected at an epoch boundary.** Accounts that satisfy step 2 are recorded
   in `EligibleValidators`. The active validator set is recomputed only when
   `height % epochLength == 0` and the height is not zero (`core/epochs.go`
   `ProcessBlockLifecycle`, `applyValidatorSelection`). With validator rotation
   disabled, which is the only configuration production code creates
   (`core/epoch/config.go` `DefaultConfig`: `RotationEnabled: false`; nothing
   outside tests calls `SetEpochConfig`), every eligible account whose latest
   heartbeat is recent enough joins the set, with no cap. Its BFT voting power
   is its `basis`. If no account qualifies, `fallbackValidatorSet`
   (`core/epochs.go`) builds the set instead from the previous active set, the
   eligible accounts and past epoch selections. It still requires registration,
   the minimum stake, not being delegated away, and at least one heartbeat ever,
   but it skips the freshness window. The result replaces the set only if it is
   not empty.

   `epochLength` in this code path is `epoch.DefaultConfig().Length`
   = **100 blocks**. The `EpochLengthBlocks = 120` value in `config.toml` belongs
   to the POTSO reward epoch (`[potso.rewards]`), not to validator selection.

An account that stops meeting step 2 is removed from the active set
immediately, in the transaction that changes it (`setAccount` deletes it from
`ValidatorSet`); joining waits for the next epoch boundary.

**Heartbeat freshness.** `validatorReadyForActivation` (`core/epochs.go`)
requires the account to be registered, to have sent at least one heartbeat, the
last heartbeat to be no more than two minutes ahead of the epoch boundary block
time, and no more than the grace period before it. The grace period is
`max(5 x heartbeat interval, 15 minutes)`; the heartbeat interval is one minute
(`core/engagement/config.go` `DefaultConfig`), so the grace period is 15 minutes
(`validatorReadinessMinGrace`, `core/epochs.go`).

## Pre-flight checklist

1. **Ports.** The P2P server listens on TCP only (`p2p/server.go`, `Start`:
   `net.Listen("tcp", ...)`; there is no UDP listener in the P2P code).
   - `22/tcp` for your own SSH access.
   - `6001/tcp` for P2P. This is the port in `ListenAddress = "0.0.0.0:6001"`
     that the bootstrap script writes.
   - The JSON-RPC port (`8545`) is bound to `127.0.0.1` by default
     (`--rpc-addr` in the script, `RPCAddress` in `config.toml`). Without TLS
     certificates the RPC server refuses to start unless
     `RPCAllowInsecure = true`, and with it set it starts only on a loopback
     address (an unspecified `0.0.0.0` bind is accepted only with
     `RPCAllowInsecureUnspecified = true`) (`rpc/http.go`, `Start`). To serve it
     on another address, configure `RPCTLSCertFile` / `RPCTLSKeyFile`.
2. **Memory and disk.** The script adds a 4 GiB swap file when the host has less
   than 4 GiB of RAM and no swap (`install_prerequisites`), because the build is
   killed on a small host without it. The script refuses a snapshot that
   is larger than `--max-snapshot-gib` (16 by default) or that the disk has no room
   for.
3. **At least 10,000 ZNHB** (the default `staking.minimumValidatorStake`),
   sent to the validator's own address once you know it (printed at the end of
   Step 1). It can be staked from the validator key itself (Step 2) or
   delegated to the address from any other wallet.
4. **No NHB is needed for the validator's own heartbeat or beneficiary
   transactions.** `TxTypeHeartbeat` and `TxTypeSetRewardBeneficiary` go through
   `handleNativeTransaction` (`core/state_transition.go`), which contains no fee
   debit for them (the fee is applied only in the transfer and EVM paths that call
   `applyTransactionFee`). The `nhb-cli` commands set `GasLimit` and `GasPrice` on
   the transaction anyway.
5. **An RPC bearer token for `nhb-cli`.** `nhb-cli` submits every transaction
   through `nhb_sendTransaction`, which requires a bearer JWT, and `nhb-cli`
   refuses to send unless the `NHB_RPC_TOKEN` environment variable is set
   (`cmd/nhb-cli/main.go`, `doRPCRequest`: `privileged RPC call requires
   NHB_RPC_TOKEN to be set`). The node verifies the token with the `[RPCJWT]`
   settings in `config.toml`: HS256, secret read from the environment variable
   named by `HSSecretEnv` (`NHB_RPC_JWT_SECRET`), issuer `nhb-rpc`, audience
   `wallets`. `nhb-cli rpc-token` signs such a token from the secret (read from
   `NHB_RPC_JWT_SECRET`, or from standard input with `--secret-stdin`); its
   lifetime is 10 minutes by default (`--ttl`, at most 24 hours). The bootstrap
   script writes the secret to `/etc/nhbchain/node.env`, and makes and passes the
   token for its own two `nhb-cli` calls; for a command you run later, Step 2
   shows how to make one.
6. **`nhb-cli` talks to `http://localhost:8080` unless told otherwise**
   (`cmd/nhb-cli/main.go`, `defaultRPCEndpoint`). The validator's RPC is on
   `127.0.0.1:8545`, so set `RPC_URL` (the commands below do) or pass a global
   `--rpc` flag (`applyGlobalFlags`).

## Step 1 - Run the bootstrap script

You need two things from whoever operates the network, neither of which is built
into the script: the location of a snapshot (`--snapshot-url`) and a bootnode
(`--bootnode`). The snapshot's manifest names the source commit the node has to
be built from. On a clean machine `nhb-snapshot` does not exist yet, so read the
commit from the manifest, which is JSON, with `curl` (the field is
`producer.binaryCommit`):

```bash
curl -fsS https://SNAPSHOT-HOST.example/PATH/manifest.json | sed -n 's/.*"binaryCommit": *"\([0-9a-f]*\)".*/\1/p'
```

**Check that this commit is a release you recognise before you check it out.**
The manifest is not signed: whoever hosts it chooses the commit, and an old commit
is old consensus code. Compare it with what the people who run the network
announce, and pass the oldest release you accept as `--min-release-commit`, so
that the script refuses a manifest that names anything older or on another
branch. Then check the commit out and run. The script you run is the one in that
commit, so the commit has to contain this procedure (`scripts/deployvalidator.sh`
as described here and `cmd/nhb-snapshot`); see
[What you need](snapshot-onboarding.md#what-you-need).

```bash
bash scripts/validator-only-bootstrap.sh \
  --beneficiary nhb1youroperatorwalletaddresshere \
  --snapshot-url https://SNAPSHOT-HOST.example/PATH \
  --bootnode BOOTNODE-HOST.example:6001 \
  --max-snapshot-age 72h \
  --min-release-commit FULL_COMMIT_ID_OF_THE_OLDEST_RELEASE_YOU_ACCEPT
```

`scripts/validator-only-bootstrap.sh` only `exec`s `scripts/deployvalidator.sh`
with the same arguments. `--max-snapshot-age 72h` refuses a snapshot whose newest
block is older than three days; without it a snapshot of any age is accepted and a
stale one fails only later, with a stall. `--min-release-commit` needs the full
history (a plain `git clone`, not `--depth 1`). Every step stops the script with
an error when it fails, and it is safe to run again: what is already in place is
left alone, and a node that is already running is restarted only if its binary,
config or key changed. A node whose data directory already holds the chain is not
given a snapshot, so a run that installs none does not fetch a manifest and does
not depend on the snapshot host at all.

**`--reset-state`** means "this node's data directory is broken": the script
downloads, verifies and unpacks a snapshot next to the old data directory, and only
then stops the node, moves the old data directory aside (it is never deleted) and
puts the snapshot in its place, keeping the node's p2p identity and vote state.
It works on a node that is already a validator; a snapshot that does not check out
changes nothing. It is not a way to start from an empty directory.

### Flags

| Flag | Default | Notes |
|---|---|---|
| `--beneficiary` | none, **required** | Wallet that receives this validator's epoch reward payouts. The script exits with an error if omitted, and the value must match `nhb1` plus 20 to 80 lower-case letters or digits. It must differ from the validator's own address; the chain rejects a beneficiary equal to the sender (`applySetRewardBeneficiary`, `core/state_transition.go`). |
| `--snapshot-url` | none, **required** (or `NHB_SNAPSHOT_URL`) | Directory URL that holds `manifest.json` and the archive it names. `https` only unless `--allow-insecure-http`. |
| `--bootnode` | none, **required** (or `NHB_BOOTNODE`) | Plain `host:port`, never an `enode://` URI (see below). Written to both `Bootnodes` and `PersistentPeers`. |
| `--min-release-commit` | none (or `NHB_MIN_RELEASE_COMMIT`); advised | The full commit id of the oldest release you accept. A manifest whose commit is neither that commit nor a descendant of it is refused. Needs both commits in your checkout (`git fetch` first). |
| `--tip-rpc` | none (or `NHB_TIP_RPC_URL`) | RPC URL of a node you trust. Used to decide when the node has caught up, and to refuse a node whose newest blocks are not that node's. |
| `--max-lag-blocks` | `3` | How far from that node (either side) still counts as caught up; at most 15 with `--tip-rpc`. |
| `--sync-timeout` | `7200` | Seconds to wait for the catch-up. |
| `--rpc-timeout` | `180` | Seconds the node's RPC may stay silent after the service starts. A node that never answers is not running or is crash-looping: the script stops after this long with the commands that show why. |
| `--max-snapshot-age` | none; use `72h` | Refuse a snapshot whose newest block is older than this, for example `72h`. The limit rests on the date of the newest block, checked against the unpacked database. |
| `--tip-hash`, `--state-root` | none | The tip hash and state root the snapshot must have (32 bytes of hex), as read from nodes you trust. A snapshot with another tip is refused before it is downloaded. |
| `--max-snapshot-gib` | `16` | Refuse a snapshot whose archive, or whose unpacked database, is larger than this many GiB, or that the disk has no room for. |
| `--listen-addr` | `0.0.0.0:6001` | Written to `ListenAddress`. |
| `--rpc-addr` | `127.0.0.1:8545` | Written to `RPCAddress`; also used by the script's own checks and `nhb-cli` calls. |
| `--external-address` | auto-detected | Publicly dialable IP (or `host:port`). If omitted the script asks the EC2 instance metadata service, then `https://ifconfig.me`. If both fail, `ExternalAddress` stays empty and the script warns. |
| `--reset-state` | off | Replace the data directory with a snapshot, as described above. |
| `--allow-insecure-http` | off | Accept an `http://` or `file://` snapshot URL. |
| `--allow-existing-key` | off | Use a validator key that was already at `/etc/nhbchain/validator.key` although the script did not create it. Only if that key runs nowhere else. |
| `--allow-binary-mismatch` | off | Continue although this build is not the snapshot's. |
| `--network-id` | the pinned chain id | Accepted only when it equals the chain id this release is pinned to (`18346390202490284624`); any other value stops the script. |
| `--help` | | Prints usage. |

The script also stops if `NHB_MASTER_TREASURY` is set in its environment.

**Bootnode format.** The dialer does `net.Dial("tcp", addr)` on the string
(`p2p/server.go` `defaultDialer`), so the value must be `host:port`. An
`enode://...` URI fails with `too many colons in address`; the script refuses it
before it starts.

**`[p2p] NetworkId` has no effect on peering.** The handshake's chain ID is
`binary.BigEndian.Uint64(genesisHash[:8])` computed from the loaded genesis
(`core/blockchain.go`), and `[p2p] NetworkId` is parsed into `cfg.P2P.NetworkID`
(`config/config.go`) but the p2p server is configured from the chain id
(`p2p/server.go`, `ChainID`). A node peers with nodes that share its genesis
hash.

### What the script does, in order

Verified against `scripts/deployvalidator.sh` (`main`):

1. Validates its arguments before touching the system, refuses to run from inside
   `/opt/nhbchain`, `/var/lib/nhbchain` or `/etc/nhbchain`, takes a lock that
   keeps two runs apart, and refuses to go on while another `nhb` process runs
   outside `nhb.service`.
2. Installs `rsync`, `perl` and `curl` with `apt-get` if missing, and Go 1.24.3
   into `/usr/local/go` if `/usr/local/go/bin/go` is missing (the tarball's sha256
   is checked before it is unpacked). Adds the 4 GiB swap file described above when
   it applies.
3. Creates the `nhb` system user, `/etc/nhbchain` (owner `nhb:nhb`, mode `700`)
   and `/var/lib/nhbchain`, copies the repo to `/opt/nhbchain` with
   `rsync -a --delete`, checks that `config/genesis.relaunch.json` is byte for
   byte the live genesis, and builds `nhb`, `nhb-cli` and `nhb-snapshot` from your
   checkout as root in `/var/cache/nhbchain-build` (a directory only root can
   write, with disk-backed `GOCACHE`, `GOPATH` and `GOTMPDIR`). Copies of the
   binaries go to `/opt/nhbchain/bin` for the service user; the tools the script
   runs itself (`nhb-cli generate-key`, `nhb-snapshot check-config` and
   `wait-synced`) are the ones in `/var/cache/nhbchain-build/bin`.
4. When it will install a snapshot (an empty data directory, or `--reset-state`),
   fetches the snapshot manifest and checks that it is for the pinned chain id and
   genesis hash (and the tip you pinned, if you did), that the commit it names is
   the release you pinned or built on it, and that the node just built is the
   binary the snapshot was taken with.
5. If `/etc/nhbchain/validator.key` does not exist, runs `nhb-cli generate-key`
   and installs the result there (mode `0600`, owner `nhb`). An existing key is
   reused; a key file the script did not create is refused on a host with no node
   data unless `--allow-existing-key` is given. The script never accepts a key as
   a flag or environment variable.
6. Downloads the archive, verifies it, checks that the key is not already a
   validator in the snapshot's state (a first run only), and unpacks it into
   `/var/lib/nhbchain/nhb-data`, never over data that is already there except
   through `--reset-state`.
7. Copies the repo's `config.toml` to `/etc/nhbchain/config.toml` (mode `0600`)
   and rewrites these keys: `ListenAddress`, `RPCAddress`, `DataDir`
   (`/var/lib/nhbchain/nhb-data`), `GenesisFile`
   (`/opt/nhbchain/config/genesis.relaunch.json`), `ValidatorKeystorePath` (set to
   `""`), `ValidatorKMSEnv` (`NHB_VALIDATOR_RAW_KEY`), `NetworkName`
   (`nhb-mainnet-validator`), `[p2p] NetworkId`, `[p2p] Bootnodes` /
   `PersistentPeers`, and `[p2p] ExternalAddress` (if known). Everything else,
   including `QuorumCertActivationHeight`, is copied unchanged. It then checks the
   values consensus depends on with `nhb-snapshot check-config`.
8. Writes `/etc/nhbchain/node.env` (mode `600`, owner `root:root`) containing
   `NHB_ENV=prod`, `NHB_RPC_JWT_SECRET` (the secret of an earlier run is kept) and
   `NHB_VALIDATOR_RAW_KEY` (the hex of the key file). Installs
   `deploy/systemd/nhb.service` from your checkout (runs
   `/opt/nhbchain/bin/nhb --config /etc/nhbchain/config.toml` as user `nhb`,
   `Restart=on-failure`), then starts or restarts it.
9. Waits for the node's RPC (`--rpc-timeout`), checks it reports the pinned chain
   id and genesis hash, and waits until it is at the network tip: connected to a
   peer, past the snapshot's height (when this run installed one) and, with
   `--tip-rpc`, within `--max-lag-blocks` of that node and holding the same newest
   blocks as it. It exits with status 1 and diagnostics commands if the RPC never
   comes up, the node stops making progress (15 minutes), the timeout passes, or
   the node is on another chain. (Health checks of the RPC must be a **POST**: a
   bare `GET` returns 400 even on a healthy node.)
10. Only then, with a short-lived RPC token (`nhb-cli rpc-token --secret-stdin
    --ttl 10m`, made from the node's own secret and passed to `nhb-cli` in its
    environment), runs as the `nhb` user `nhb-cli set-reward-beneficiary
    <beneficiary> <validator.key>` and `nhb-cli register-validator 0
    <validator.key>` (registration with no added stake), each tried up to three
    times. If a step fails, the script prints the command that makes a token and
    the exact retry commands (the same form as in Step 2 below) and exits with
    status 1.
11. Prints the validator address, the address the node advertises, and next steps.

### `QuorumCertActivationHeight`

Blocks that arrive by peer sync above `QuorumCertActivationHeight` must carry a
quorum certificate that verifies against the validator set that was active at the
parent height, or the node rejects them (`core/node.go`, `commitBlock`;
`core/types/vote.go` `QuorumCert.Verify`: strictly more than two thirds of voting
power). Blocks at or below the height sync without one. A value of `0` (or a
missing line) leaves the check off (`cmd/nhb/main.go` only applies it when greater
than zero, and the node's own default is never to verify). The shipped
`config.toml` sets it to `0`, and `nhb-snapshot check-config` checks that value.
It must be the same on every validator. Check it with:

```bash
grep QuorumCertActivationHeight /etc/nhbchain/config.toml
```

## Step 2 - Stake and get paid

### Staking

Two ways to bring the validator address up to the minimum stake; both count:

- **Self-stake on the server** using the validator's own key. First send at
  least 10,000 ZNHB to the validator address with an ordinary transfer, then make
  a token and run the command as the service user (the key belongs to it). The
  token lasts ten minutes; make a new one for the next command:

  ```bash
  TOKEN=$(sudo sed -n 's/^NHB_RPC_JWT_SECRET=//p' /etc/nhbchain/node.env | sudo -u nhb /opt/nhbchain/bin/nhb-cli rpc-token --secret-stdin)
  NHB_RPC_TOKEN="$TOKEN" RPC_URL=http://127.0.0.1:8545 sudo --preserve-env=NHB_RPC_TOKEN,RPC_URL -u nhb /opt/nhbchain/bin/nhb-cli register-validator 10000000000000000000000 /etc/nhbchain/validator.key
  ```

  The amount is in base units (18 decimals), so `10000000000000000000000` is
  10,000 ZNHB. The command signs a `TxTypeStake` with `registerValidator = true`
  and `Value = amount` (`cmd/nhb-cli/validator_registration.go`). `0` means
  register with no added stake. It can be run again later with a further amount.
- **Delegate from any wallet** to the validator address by submitting a
  `TxTypeStake` whose payload names the validator. `StakeDelegate`
  (`core/state_transition.go`) locks the delegator's ZNHB and adds the amount to
  the validator account's `Stake`. `nhb-cli stake <amount> <key>` builds a
  `TxTypeStake` with no payload, so it self-stakes; it cannot target another
  validator.

Registration is separate from stake: delegation cannot set the
`ValidatorRegistered` flag, so a delegated-only node still needs
`register-validator 0` (or any `register-validator <amount>`) run with its own
key. The bootstrap script does this once (step 10 above).

A validator account that delegates its own stake to a different validator fails
`selfDelegated` and is not eligible. Unstaking below the minimum, or
`nhb-cli deregister-validator <key>`, removes it from the active set immediately
(`applyUnstake`, `core/state_transition.go`).

### Reward beneficiary

`nhb-cli set-reward-beneficiary <address> <key-file>` signs a
`TxTypeSetRewardBeneficiary` (0x1A) with the validator key. When the epoch reward
settlement pays this validator, the amount is credited to the beneficiary address
instead of the validator address (`core/rewards_logic.go`). An empty string clears
the beneficiary. The beneficiary cannot be the validator's own address. Run it on
the server with the local key file, with a token made as above; the key should
not leave the server:

```bash
TOKEN=$(sudo sed -n 's/^NHB_RPC_JWT_SECRET=//p' /etc/nhbchain/node.env | sudo -u nhb /opt/nhbchain/bin/nhb-cli rpc-token --secret-stdin)
NHB_RPC_TOKEN="$TOKEN" RPC_URL=http://127.0.0.1:8545 sudo --preserve-env=NHB_RPC_TOKEN,RPC_URL -u nhb /opt/nhbchain/bin/nhb-cli set-reward-beneficiary <your-wallet-address> /etc/nhbchain/validator.key
```

## Step 3 - Check status

- **Registration, stake and last heartbeat:** `nhb_getValidatorInfo` takes one
  parameter, an address, either bech32 (`nhb1...`, `znhb1...`) or `0x` hex
  (`parseValidatorAddress`, `rpc/explorer_handlers.go`). It returns `address`
  (hex), `stake`, `engagementScore`, `validatorRegistered`,
  `validatorRegisteredAt`, `engagementLastHeartbeat`, `delegatedValidator`,
  `nonce`.
- **Account view:** `nhb-cli balance <address>` prints Staked, Locked, Delegated
  Validator, `Validator Registered: yes|no`, pending unbonds and nonce
  (`cmd/nhb-cli/main.go`, `getBalance`).
- **Active set:** `nhb_getValidatorSet` takes optional `[offset, limit]` (default
  limit 100, max 500) and returns `validators` (each with `address` and `stake` =
  voting power), `totalCount`, `offset`, `limit`, `hasMore`, `timestamp`.
  `nhb_getNetworkStats` returns `activeValidators`, `currentEpoch`, `currentTime`,
  `mempoolSize`, `tps`.
- **Service:**

  ```bash
  sudo systemctl status nhb.service
  sudo journalctl -u nhb.service -f
  ```

The node sends its own heartbeats: `cmd/nhb/main.go` `startValidatorHeartbeatLoop`
submits one 5 seconds after start and then checks every minute. It submits when the
on-chain last heartbeat is older than the heartbeat interval plus a 15-second
margin, and at most once per that period per process (`core/node.go`
`EngagementValidatorHeartbeatDue`, `HeartbeatSubmissionMargin`). If a heartbeat is
still pending in the mempool at the same nonce, the retry raises the gas price above
the pending one so replace-by-fee accepts it (`core/node.go`
`EngagementSubmitHeartbeat`).

Epoch length for validator selection is 100 blocks, so an epoch lasts 100 times the
block interval. The block interval is not fixed: it is set by each validator's
`[consensus] MinBlockInterval` (default 1 s when the key is absent) and what a
height itself takes; see [Block cadence](../consensus/block-cadence.md). Watch
`currentEpoch` and the height from `nhb_getNetworkStats`.

## Troubleshooting

Each item below is the symptom, the cause in the script or code, and the fix.

**Build is killed (`signal: killed`).** The build ran out of memory. The script
adds a 4 GiB swap file only when the host has under 4 GiB of RAM and no swap. Add
swap yourself or build on a larger host.

**Build fails with `no space left on device`.** Go's scratch directory defaulted to
`/tmp`. The script builds with `GOCACHE`, `GOPATH` and `GOTMPDIR` under
`/var/cache/nhbchain-build`; if you build by hand, do the same.

**`nhb.service` crash-loops with `Failed to load config ... permission denied`.**
`/etc/nhbchain` must be owned by `nhb`, the user the unit runs as
(`deploy/systemd/nhb.service`). Fix: `chown -R nhb:nhb /etc/nhbchain`.

**The script stops with `the node's RPC did not come up within ... seconds`.**
`nhb.service` is not running or is crash-looping (`Restart=on-failure` restarts it
every 5 seconds). Read `sudo journalctl -u nhb.service -n 80`.

**Bootnode dial fails with `too many colons in address`.** An `enode://` URI was
used. Use `host:port`.

**Heartbeat nonce never advances.** A heartbeat at the same gas price as one
still pending is rejected by replace-by-fee. The node bumps the price on retry
(see Step 3).

More entries, including the snapshot checks, are in
[Onboarding a validator from a snapshot](snapshot-onboarding.md#troubleshooting).

## Common questions

**Do I need NHB to send heartbeats or set the beneficiary?** No; see pre-flight
item 4.

**I registered and staked; why is the validator not active?** The active set
changes only at epoch boundaries (every 100 blocks), and only includes accounts
whose last heartbeat is within 15 minutes of the boundary block time.

**I delegated 10,000 ZNHB from a wallet; why is it not active?** Delegation
counts toward the stake threshold, but the validator's own registration flag is
separate and can only be set by the validator key: run
`nhb-cli register-validator 0 <key>` on the server (with a token, as in Step 2),
and check `validatorRegistered` with `nhb_getValidatorInfo`.

**Where do I set the reward beneficiary?** With `nhb-cli set-reward-beneficiary`
and the validator's own key file; the transaction is only valid when signed by the
validator key.
