# Governance Devnet Runbook

Goal: take one `param.update` proposal through propose, vote, finalize, queue
and execute on a node you control, using `nhb-cli`. Every command and flag
below is taken from `cmd/nhb-cli` and `cmd/nhb`; the node-side setup (genesis,
keystore, RPC auth, POTSO epochs) depends on your configuration and is
described only where the governance code depends on it.

## 1. Build

Go 1.24 or newer (`go.mod`).

```bash
git clone https://github.com/josephblackelite/nhbchain.git
cd nhbchain
go build -o bin/nhb-node ./cmd/nhb/
go build -o bin/nhb-cli ./cmd/nhb-cli/
```

## 2. Node configuration

Start from the repository's `config.toml` and replace the `[governance]` block
with short timings so the lifecycle finishes in minutes. `AllowedParams` must
contain the key you will change, and its keys need validators (see
[params](./params.md)); this example uses `staking.aprBps`.

```toml
[governance]
  MinDepositWei = "1000000000000000000"   # 1 ZNHB
  VotingPeriodSeconds = 120
  TimelockSeconds = 60
  QuorumBps = 1000
  PassThresholdBps = 5000
  AllowedParams = ["staking.aprBps"]
```

Notes from the code:

- These values are read from the local file by each validator
  (`GovConfig.Policy()`); every validator must run the same block.
- `cmd/nhb` does not enforce a minimum voting period on `[governance]`
  (`cmd/consensusd` validates `[global.Governance]`, which is a separate block,
  see [policy invariants](../gov/policy-invariants.md)).
- `cmd/nhb` flags: `--config <path>` (default `./config.toml`), `--genesis
  <path>`, `--allow-autogenesis` (development only, also `NHB_ALLOW_AUTOGENESIS`),
  `--allow-migrate`. The genesis file is taken from `--genesis`, else the
  `NHB_GENESIS` environment variable, else `GenesisFile` in the config
  (`resolveGenesisPath`, `cmd/nhb/main.go`). With none of them the node refuses
  to start unless autogenesis is enabled.
- The repository's `config.toml` sets `GenesisFile =
  "./config/genesis.relaunch.json"`, the genesis of the live network (also
  embedded in the binary as `config.MainnetGenesis`, and written to the
  configured path when that file is missing). Do not use it for a devnet: give
  the devnet its own genesis file with `--genesis`, one that allocates ZNHB to
  the proposer and voter (`config/genesis.local.json` shows the format). `--allow-autogenesis` only applies when no genesis path is
  set at all, and then creates an empty genesis block with no balances
  (`createGenesisBlock`, `core/blockchain.go`), so no account could pay a
  deposit.

```bash
bin/nhb-node --config ./devnet.toml --genesis ./devnet-genesis.json
```

## 3. CLI setup

`nhb-cli` talks to `http://localhost:8080` unless `RPC_URL` is set or `--rpc
<url>` is given (the flag is removed from the arguments wherever it appears).
Write methods (`nhb_sendTransaction`) send `Authorization: Bearer
$NHB_RPC_TOKEN`; the CLI refuses to send a transaction without that variable
set. On the node host, `nhb-cli rpc-token` prints a short-lived token signed
with the node's JWT secret, read from the `NHB_RPC_JWT_SECRET` environment
variable or from standard input with `--secret-stdin` (flags `--ttl`, default
10 minutes and at most 24 hours; `--issuer`, default `nhb-rpc`; `--audience`,
default `wallets`; these must match the node's `[RPCJWT]` block,
`cmd/nhb-cli/rpc_token.go`).

```bash
export RPC_URL="http://127.0.0.1:8081"
export NHB_RPC_TOKEN="$(bin/nhb-cli rpc-token)"   # or any token the node's RPC auth accepts
```

Create keys. `generate-key` writes `./wallet.key` and prints the address. It
never overwrites an existing `wallet.key` (it exits non-zero instead;
`generate-key --force` first saves a `wallet.key.bak-<UTC time>` copy), so
rename the file after each run. Read an address back with `address`:

```bash
bin/nhb-cli generate-key && mv wallet.key proposer.key
bin/nhb-cli generate-key && mv wallet.key voter.key
proposer=$(bin/nhb-cli address proposer.key)
voter=$(bin/nhb-cli address voter.key)
```

Preconditions for the governance transactions:

- The proposer needs at least `MinDepositWei` in **ZNHB** (the deposit is
  debited from `BalanceZNHB`). Send ZNHB from a funded key with
  `bin/nhb-cli send-znhb <recipient> <amount_wei> <key_file>`.
- Every voter needs non-zero POTSO weight in the snapshot of the last processed
  POTSO reward epoch. Check with the `potso_getWeight` RPC, params
  `[{"address": "<bech32>"}]`, result `{epoch, address, weightBps}`. POTSO
  reward epochs run every `[potso.rewards] EpochLengthBlocks` blocks (`0`
  disables them; the shipped `config.toml` sets `120`), so how long the first
  one takes depends on the block pace ([block cadence](../consensus/block-cadence.md)).
  If no epoch has been processed, votes fail with `governance: potso snapshot
  unavailable`; a voter not in the snapshot fails with `governance: voter has
  zero voting power`.

## 4. Proposal lifecycle

### 4.1 Payload

```bash
cat > payload.json <<'JSON'
{"staking.aprBps": 1000}
JSON
```

A bare number and a quoted decimal string are both accepted and are read back the same way (see [params](./params.md#how-a-parameter-proposal-is-checked)); this example uses a bare number.

### 4.2 Propose

```bash
bin/nhb-cli gov propose \
  --kind param.update \
  --payload @payload.json \
  --key proposer.key \
  --deposit 1000000000000000000
```

Prints `Broadcasted governance proposal: <tx hash>`. `--deposit` is in wei and
accepts forms such as `1e18`. Find the proposal id:

```bash
bin/nhb-cli gov list
bin/nhb-cli gov show --id 1
```

`status` is `2` (voting_period) until finalized
([API](./api.md#gov_proposal)).

### 4.3 Vote

```bash
bin/nhb-cli gov vote --id 1 --key voter.key --choice yes
```

`--choice` is `yes`, `no` or `abstain` (case-insensitive). A second vote from
the same key replaces the first.

### 4.4 Finalize

Voting ends at the proposal's `voting_end` (submission time plus
`VotingPeriodSeconds`). After that:

```bash
bin/nhb-cli gov finalize --id 1 --key proposer.key
bin/nhb-cli gov show --id 1
```

`status` becomes `3` (passed) when `turnout_bps >= quorum_bps` and
`yes_ratio_bps >= pass_threshold_bps`, otherwise `4` (rejected). Finalizing
earlier fails with `governance: voting still in progress`. A passed proposal's
deposit is returned to the proposer.

### 4.5 Queue and execute

```bash
bin/nhb-cli gov queue --id 1 --key proposer.key
# wait until timelock_end
bin/nhb-cli gov execute --id 1 --key proposer.key
```

`timelock_end` is fixed at submission (`voting_end + TimelockSeconds`). Execute
before then fails with `governance: timelock not yet elapsed`. Finalize, queue
and execute may be sent by any funded key.

### 4.6 Verify

```bash
bin/nhb-cli gov show --id 1      # status 7 = executed
```

The applied value is stored under `params/staking.aprBps` in state. To read it
back through the consensus query API use `QueryState("gov", "params")`, which
returns the stored values for keys in `AllowedParams` (see
[state indexes](./state-indexes.md)). The `gov.executed` event and the audit
record at `gov/audit/<n>` list the changed keys.

## 5. Cleanup

Stop the node and delete its data directory and the `*.key` files you created.
