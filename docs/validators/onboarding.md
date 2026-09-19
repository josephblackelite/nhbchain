# Validator Onboarding Guide

This guide walks through standing up an NHBChain validator on a fresh Ubuntu
server with `scripts/validator-only-bootstrap.sh`, and then explains, from the
code, what makes that node a validator: registration, stake, heartbeats and
epoch selection. Every statement here is taken from the source in this
repository; where behaviour is not defined in code this guide does not describe
it.

The one-command version is in the repo README under
["Join As A Validator In One Command"](../../README.md#join-as-a-validator-in-one-command).

## How a node becomes an active validator

A node's validator key is a secp256k1 key. Its address is the account that
carries the validator's state. Three separate things have to be true, and they
are checked in different places:

1. **Registered.** The account's `ValidatorRegistered` flag is set. The flag is
   set only by a transaction the validator key itself signs: a `TxTypeStake`
   whose payload has `registerValidator = true` and no third-party validator
   target (`core/state_transition.go`, `applyStake`, ~line 6507). No other
   account can set it. `nhb-cli register-validator` builds that transaction.
2. **Enough stake, not delegated away.** `core/state_transition.go` `setAccount`
   (~line 7266) computes
   `meetsStake := registered && selfDelegated(account, addr) && basis >= minStake`.
   - `basis` is the account's total `Stake`, which includes ZNHB delegated in by
     any other wallet with nothing subtracted (`validatorEligibilityBasis`,
     ~line 6717; its doc comment says there is no separate self-stake
     requirement). Delegating at least the minimum to a node address from any
     wallet therefore counts, and so does self-staking, and so does a mix.
   - `selfDelegated` is true when the account is not delegating its own stake
     to a different validator (`DelegatedValidator` empty or equal to itself,
     ~line 6748).
   - `minStake` is the governance parameter `staking.minimumValidatorStake`;
     when unset it is `10000000000000000000000` base units = 10,000 ZNHB
     (`native/governance/types.go` `defaultMinimumValidatorStakeWei`, line 402;
     read by `minimumValidatorStake`, `core/state_transition.go` ~line 6997).
     `[global.Staking].MinStakeWei` in `config.toml` is only format-checked at
     startup and does not set this threshold (`core/node.go`
     `ValidateStakingConfig`, ~line 1604).
   - Reward accrual is different: `stakeRewardBasis` (~line 6674) excludes
     delegated-in stake, so a validator earns staking rewards only on capital
     it staked itself.
3. **Selected at an epoch boundary.** Accounts that satisfy step 2 are recorded
   in `EligibleValidators`. The active validator set is recomputed only when
   `height % epochLength == 0` (`core/epochs.go` `ProcessBlockLifecycle`,
   ~line 251, and `applyValidatorSelection`, ~line 374). With validator
   rotation disabled, which is the only configuration production code creates
   (`core/epoch/config.go` `DefaultConfig`: `RotationEnabled: false`; nothing
   outside tests calls `SetEpochConfig`), every eligible account whose latest
   heartbeat is recent enough joins the set, with no cap. Its BFT voting power
   is its `basis`. If no account qualifies, `fallbackValidatorSet`
   (`core/epochs.go` line 484) builds the set instead from the previous active
   set, the eligible accounts and past epoch selections. It still requires
   registration, the minimum stake, not being delegated away, and at least one
   heartbeat ever, but it skips the freshness window. The result replaces the
   set only if it is not empty (`applyValidatorSelection`, `core/epochs.go`
   lines 447-454).

   `epochLength` in this code path is `epoch.DefaultConfig().Length`
   = **100 blocks**. The `EpochLengthBlocks = 120` value in `config.toml` belongs
   to the POTSO reward epoch (`[potso.rewards]`), not to validator selection.

An account that stops meeting step 2 is removed from the active set
immediately, in the transaction that changes it (`setAccount` deletes it from
`ValidatorSet`); joining waits for the next epoch boundary.

**Heartbeat freshness.** `validatorReadyForActivation` (`core/epochs.go`
~line 558) requires the account to be registered, to have sent at least one
heartbeat, and for the last heartbeat to be no more than the grace period
before the epoch boundary block time. The grace period is
`max(5 x heartbeat interval, 15 minutes)`; the heartbeat interval is one
minute (`core/engagement/config.go` `DefaultConfig`), so the grace period is
15 minutes (`validatorReadinessMinGrace`, `core/epochs.go` line 16).

## Pre-flight checklist

1. **Ports.** The P2P server listens on TCP only (`p2p/server.go`, `Start`:
   `net.Listen("tcp", ...)`; there is no UDP listener in the P2P code).
   - `22/tcp` for your own SSH access.
   - `6001/tcp` for P2P. This is the port in `ListenAddress = "0.0.0.0:6001"`
     that the bootstrap script writes.
   - The JSON-RPC port (`8545`) is bound to `127.0.0.1` by default
     (`--rpc-addr` in the script, `RPCAddress` in `config.toml`). Without TLS
     certificates the RPC server refuses to start unless
     `RPCAllowInsecure = true` (`rpc/http.go`, lines 840-843), and with it set
     it starts only on a loopback address (an unspecified `0.0.0.0` bind is
     accepted only with `RPCAllowInsecureUnspecified = true`; lines 844-865). To
     serve it on another address, configure `RPCTLSCertFile` / `RPCTLSKeyFile`.
2. **At least 10,000 ZNHB** (the default `staking.minimumValidatorStake`),
   sent to the validator's own address once you know it (printed at the end of
   Step 1). It can be staked from the validator key itself (Step 2) or
   delegated to the address from any other wallet.
3. **No NHB is needed for the validator's own heartbeat or beneficiary
   transactions.** `TxTypeHeartbeat` and `TxTypeSetRewardBeneficiary` go through
   `handleNativeTransaction` (`core/state_transition.go`, ~line 3927), which
   contains no fee debit for them. The `nhb-cli` commands set `GasLimit` and
   `GasPrice` on the transaction anyway.
4. **An RPC bearer token for `nhb-cli`.** `nhb-cli` submits every transaction
   through `nhb_sendTransaction`, which requires a bearer JWT
   (`rpc/http.go`, ~line 1381), and `nhb-cli` refuses to send unless the
   `NHB_RPC_TOKEN` environment variable is set
   (`cmd/nhb-cli/main.go`, lines 20 and 638-641). The node verifies the token
   with the `[RPCJWT]` settings in `config.toml`: HS256, secret read from the
   environment variable named by `HSSecretEnv` (`NHB_RPC_JWT_SECRET`), issuer
   `nhb-rpc`, audience `wallets`. `generate_jwt.go` in the repo root
   (`//go:build ignore`) signs such a token from `NHB_RPC_JWT_SECRET`. The
   bootstrap script writes that secret to `/etc/nhbchain/node.env`.
5. **`nhb-cli` talks to `http://localhost:8080` unless told otherwise**
   (`cmd/nhb-cli/main.go`, `defaultRPCEndpoint`). The validator's RPC is on
   `127.0.0.1:8545`, so pass `--rpc http://127.0.0.1:8545` or set `RPC_URL`
   (the bootstrap script sets `RPC_URL` for its own calls).

## Step 1 - Run the bootstrap script

On the fresh server, get the source onto the box and run:

```bash
bash scripts/validator-only-bootstrap.sh \
  --beneficiary nhb1youroperatorwalletaddresshere \
  --reset-state
```

`scripts/validator-only-bootstrap.sh` only `exec`s `scripts/deployvalidator.sh`
with the same arguments.

**Pass `--reset-state` only the first time**, on a machine with no chain data
you want to keep. It runs `rm -rf /var/lib/nhbchain/nhb-data` before the first
start.

### Flags

| Flag | Default | Notes |
|---|---|---|
| `--beneficiary` | none, **required** | Wallet that receives this validator's epoch reward payouts. The script exits with an error if omitted. It must differ from the validator's own address; the chain rejects a beneficiary equal to the sender (`applySetRewardBeneficiary`, `core/state_transition.go` ~line 5424). |
| `--bootnode` | the script's built-in default (`BOOTNODE_DEFAULT`, `scripts/deployvalidator.sh` line 21) | Plain `host:port` only (see below), for example `<bootnode-host>:6001`. Written to both `Bootnodes` and `PersistentPeers`. |
| `--network-id` | `430060579445266314` | Written to `[p2p] NetworkId`. See the note below: the P2P handshake does not read this value. |
| `--listen-addr` | `0.0.0.0:6001` | Written to `ListenAddress`. |
| `--rpc-addr` | `127.0.0.1:8545` | Written to `RPCAddress`; also used by the script's own health check and `nhb-cli` calls. |
| `--external-address` | auto-detected | Publicly dialable IP (or `host:port`). If omitted the script tries to detect this machine's public IP. If detection fails, `ExternalAddress` stays empty. |
| `--reset-state` | off | Deletes local chain state before the first start. |
| `--help` | | Prints usage, including options not listed here. |

**Bootnode format.** The dialer does `net.Dial("tcp", addr)` on the string
(`p2p/server.go` `defaultDialer`), so the value must be `host:port`. An
`enode://...` URI fails with `too many colons in address`.

**`--network-id` has no effect on peering.** The handshake's chain ID is
`binary.BigEndian.Uint64(genesisHash[:8])` computed from the loaded genesis
(`core/blockchain.go`, lines 197 and 257), and `[p2p] NetworkId` is parsed into
`cfg.P2P.NetworkID` (`config/config.go` ~line 542) but nothing reads it. A node
peers with nodes that share its genesis hash.

### What the script does, in order

Verified against `scripts/deployvalidator.sh`:

1. Installs `rsync`, `perl` and `curl` with `apt-get` if missing, and Go
   1.24.3 into `/usr/local/go` if `/usr/local/go/bin/go` is missing.
2. If `/swapfile` does not exist, no swap is active, and `MemTotal` is under
   4 GiB, creates and enables a 4G swap file and adds it to `/etc/fstab`.
3. Creates the `nhb` system user, `/etc/nhbchain` (owner `nhb:nhb`, mode `700`)
   and `/var/lib/nhbchain`.
4. `rsync -a --delete` of the repo into `/opt/nhbchain`.
5. Builds `bin/nhb` from `./cmd/nhb` and `bin/nhb-cli` from `./cmd/nhb-cli`,
   with `GOCACHE`, `GOPATH` and `GOTMPDIR` under `/opt/nhbchain` so the build
   does not use `/tmp`.
6. If `/etc/nhbchain/validator.key` does not exist, runs `nhb-cli generate-key`
   and installs the result there (mode `0600`, owner `nhb`). An existing key is
   reused. The script never accepts a key as a flag or environment variable.
7. Writes `/etc/nhbchain/node.env` (mode `600`, owner `root:root`) containing
   `NHB_ENV=prod`, a freshly generated `NHB_RPC_JWT_SECRET`, and
   `NHB_VALIDATOR_RAW_KEY` (the hex of the key file).
8. Auto-detects the external address if `--external-address` was not given.
9. Copies the repo's `config.toml` to `/etc/nhbchain/config.toml` and rewrites
   these keys: `ListenAddress`, `RPCAddress`, `DataDir`
   (`/var/lib/nhbchain/nhb-data`), `ValidatorKeystorePath` (set to `""`),
   `ValidatorKMSEnv` (`NHB_VALIDATOR_RAW_KEY`), `NetworkName`
   (`nhb-mainnet-validator`), `[p2p] NetworkId`, `[p2p] ExternalAddress` (if
   known), and `[p2p] Bootnodes` / `PersistentPeers`. Everything else,
   including `QuorumCertActivationHeight`, is copied unchanged.
10. Installs `deploy/systemd/nhb.service` (runs
    `/opt/nhbchain/bin/nhb --config /etc/nhbchain/config.toml` as user `nhb`,
    `Restart=on-failure`), then `daemon-reload`, `enable`, `restart`.
11. Polls `POST http://<rpc-addr>/` with `nhb_getNetworkStats` up to 30 times,
    2 seconds apart. If it never answers, the script prints diagnostics
    commands and exits with status 1. A bare `GET` returns 400, so do not
    health-check with `curl -f` on a `GET`.
12. Runs `nhb-cli set-reward-beneficiary <beneficiary> <validator.key>` as user
    `nhb`. On failure it prints a warning and the retry command and continues.
13. Runs `nhb-cli register-validator 0 <validator.key>` (registration with no
    added stake). On failure it prints a warning and the retry command and
    continues.
14. Prints the validator address and next steps.

Steps 12 and 13 call `nhb-cli` without setting `NHB_RPC_TOKEN`; see
"Known issues" at the end of this guide.

### `QuorumCertActivationHeight`

Blocks that arrive by peer sync above `QuorumCertActivationHeight` must carry a
quorum certificate that verifies against the validator set that was active at
the parent height, or the node rejects them (`core/node.go`, ~line 3921;
`core/types/vote.go` `QuorumCert.Verify`: at least ceil(2/3) of voting power).
Blocks at or below the height sync without one. A value of `0` (or a missing
line) leaves the check off (`cmd/nhb/main.go` line 153 only applies it when
greater than zero). The repo `config.toml` sets `451949`. Every validator must
use the same value. Check it with:

```bash
grep QuorumCertActivationHeight /etc/nhbchain/config.toml
```

## Step 2 - Stake and get paid

### Staking

Two ways to bring the validator address up to the minimum stake; both count:

- **Self-stake on the server** using the validator's own key. First send at
  least 10,000 ZNHB to the validator address with an ordinary transfer, then:

  ```bash
  sudo -u nhb env RPC_URL=http://127.0.0.1:8545 NHB_RPC_TOKEN=<jwt> \
    /opt/nhbchain/bin/nhb-cli register-validator 10000000000000000000000 /etc/nhbchain/validator.key
  ```

  The amount is in base units (18 decimals), so `10000000000000000000000` is
  10,000 ZNHB. The command signs a `TxTypeStake` with `registerValidator = true`
  and `Value = amount` (`cmd/nhb-cli/validator_registration.go`). `0` means
  register with no added stake. It can be run again later with a further amount.
- **Delegate from any wallet** to the validator address by submitting a
  `TxTypeStake` whose payload names the validator. `StakeDelegate`
  (`core/state_transition.go`, ~line 5759) locks the delegator's ZNHB and adds
  the amount to the validator account's `Stake`. `nhb-cli stake <amount> <key>`
  builds a `TxTypeStake` with no payload, so it self-stakes; it cannot target
  another validator.

Registration is separate from stake: delegation cannot set the
`ValidatorRegistered` flag, so a delegated-only node still needs
`register-validator 0` (or any `register-validator <amount>`) run with its own
key. The bootstrap script attempts this once (step 13 above).

A validator account that delegates its own stake to a different validator fails
`selfDelegated` and is not eligible. Unstaking below the minimum, or
`nhb-cli deregister-validator <key>`, removes it from the active set
immediately (`applyUnstake`, `core/state_transition.go` ~line 6555).

### Reward beneficiary

`nhb-cli set-reward-beneficiary <address> <key-file>` signs a
`TxTypeSetRewardBeneficiary` (0x1A) with the validator key. When the epoch
reward settlement pays this validator, the amount is credited to the beneficiary
address instead of the validator address (`core/rewards_logic.go`, lines
286-291). An empty string clears the beneficiary. The beneficiary cannot be the
validator's own address. Run it on the server with the local key file; the key
should not leave the server.

## Step 3 - Check status

- **Registration, stake and last heartbeat:** `nhb_getValidatorInfo` takes one
  parameter, an address parsed with `common.HexToAddress`, so pass the 20-byte
  address as `0x...` hex (`rpc/explorer_handlers.go`, line 109). It returns
  `address`, `stake`, `engagementScore`, `validatorRegistered`,
  `validatorRegisteredAt`, `engagementLastHeartbeat`, `delegatedValidator`,
  `nonce`.
- **Account view:** `nhb-cli balance <address>` prints Staked, Locked,
  Delegated Validator, `Validator Registered: yes|no`, pending unbonds and
  nonce (`cmd/nhb-cli/main.go`, `getBalance`).
- **Active set:** `nhb_getValidatorSet` takes optional `[offset, limit]`
  (default limit 100, max 500) and returns `validators` (each with `address`
  and `stake` = voting power), `totalCount`, `offset`, `limit`, `hasMore`,
  `timestamp`. `nhb_getNetworkStats` returns `activeValidators`,
  `currentEpoch`, `currentTime`, `mempoolSize`, `tps`.
- **Service:**

  ```bash
  sudo systemctl status nhb.service
  sudo journalctl -u nhb.service -f
  ```

The node sends its own heartbeats: `cmd/nhb/main.go`
`startValidatorHeartbeatLoop` submits one 5 seconds after start and then checks
every minute. It submits when the on-chain last heartbeat is older than the
heartbeat interval plus a 15-second margin, and at most once per that period
per process (`core/node.go` `EngagementValidatorHeartbeatDue`, `HeartbeatSubmissionMargin`).
If a heartbeat is still pending in the mempool at the same nonce, the retry
raises the gas price above the pending one so replace-by-fee accepts it
(`core/node.go` `EngagementSubmitHeartbeat`).

Epoch length for validator selection is 100 blocks. Block time is not fixed in
the code, so this guide gives no wall-clock estimate; watch `currentEpoch` and
the height from `nhb_getNetworkStats`.

## Troubleshooting

Each item below is the symptom, the cause in the script or code, and the fix.

**Build is killed (`signal: killed`).** The build ran out of memory. The script
adds a 4G swap file only when the host has under 4 GiB of RAM and no swap. Add
swap yourself or build on a larger host.

**Build fails with `no space left on device`.** Go's scratch directory
defaulted to `/tmp`. The script sets `GOCACHE`, `GOPATH` and `GOTMPDIR` under
`/opt/nhbchain`; if you build by hand, do the same.

**`nhb.service` crash-loops with `Failed to load config ... permission denied`.**
`/etc/nhbchain` must be owned by `nhb`, the user the unit runs as
(`deploy/systemd/nhb.service`). Fix: `chown -R nhb:nhb /etc/nhbchain`.

**Bootnode dial fails with `too many colons in address`.** An `enode://` URI was
used. Use `host:port`.

**Heartbeat nonce never advances.** A heartbeat at the same gas price as one
still pending is rejected by replace-by-fee. The node bumps the price on retry
(see Step 3). If you see it on an older build, update.

**The script printed success but the node is not running.** The script now
exits 1 if the RPC never answers within about 60 seconds; check
`systemctl status nhb.service` and `journalctl -u nhb.service`.

## Common questions

**Do I need NHB to send heartbeats or set the beneficiary?** No; see
pre-flight item 3.

**I registered and staked; why is the validator not active?** The active set
changes only at epoch boundaries (every 100 blocks), and only includes accounts
whose last heartbeat is within 15 minutes of the boundary block time.

**I delegated 10,000 ZNHB from a wallet; why is it not active?** Delegation
counts toward the stake threshold, but the validator's own registration flag is
separate and can only be set by the validator key: run
`nhb-cli register-validator 0 <key>` on the server, and check
`validatorRegistered` with `nhb_getValidatorInfo`.

**Where do I set the reward beneficiary?** With `nhb-cli set-reward-beneficiary`
and the validator's own key file; the transaction is only valid when signed by
the validator key.

## Known issues in the scripts and CLI

These are behaviours found while checking this guide against the code. They are
recorded here so operators are not surprised; the code has not been changed.

- `scripts/deployvalidator.sh` runs `nhb-cli set-reward-beneficiary` and
  `register-validator` (lines 426-427 and 445-446) as
  `sudo -u <user> env RPC_URL=... nhb-cli ...` without `NHB_RPC_TOKEN`, and
  `nhb-cli` returns an error before sending when that variable is empty
  (`cmd/nhb-cli/main.go`, lines 638-641). Those two steps therefore only warn,
  and you must run the printed retry commands with the token set.
