# Governance Devnet Runbook

> Goal: spin a single-node devnet, walk a governance proposal from creation to execution, and capture the resulting parameter change.

## 1. Prerequisites

- Go 1.24.x on your PATH
- `curl`, `jq`, and `tmux` (recommended for long running node sessions)
- Two terminal windows/tabs: one for the node, one for CLI calls

```bash
sudo apt update
sudo apt install build-essential jq tmux -y
```

Clone and build the binaries once:

```bash
git clone https://github.com/josephblackelite/nhbchain.git
cd nhbchain
go build -o bin/nhb-node ./cmd/nhb/
go build -o bin/nhb-cli ./cmd/nhb-cli/
```

## 2. One-node devnet

Create a fresh config that enables governance parameter updates and fast POTSO snapshots:

```bash
cat > devnet.toml <<'TOML'
ListenAddress = "0.0.0.0:6002"
RPCAddress = "0.0.0.0:8081"
DataDir = "./nhb-data-devnet"
GenesisFile = ""
ValidatorKeystorePath = "./validator-devnet.keystore"
NetworkName = "nhb-devnet"

[p2p]
NetworkId = 187001
MaxPeers = 32
MaxInbound = 32
MaxOutbound = 16
Bootnodes = []
PersistentPeers = []
BanScore = 100
GreyScore = 50
RateMsgsPerSec = 50
Burst = 200
HandshakeTimeoutMs = 5000

[governance]
MinDepositWei = "100000000000000000"
VotingPeriodSeconds = 120
TimelockSeconds = 60
QuorumBps = 3000
PassThresholdBps = 5000
AllowedParams = ["fees.baseFee"]

[potso.rewards]
EpochLengthBlocks = 1
AlphaStakeBps = 5000
MinPayoutWei = "0"
EmissionPerEpoch = "1000000000000000000"
TreasuryAddress = "0x0101010101010101010101010101010101010101"
MaxWinnersPerEpoch = 16
CarryRemainder = true
PayoutMode = "claim"

[potso.weights]
AlphaStakeBps = 7000
TxWeightBps = 2000
EscrowWeightBps = 500
UptimeWeightBps = 500
MaxEngagementPerEpoch = 1000
MinStakeToWinWei = "0"
MinEngagementToWin = 0
DecayHalfLifeEpochs = 1
TopKWinners = 16
TieBreak = "stake"
TOML
```

Start the node (keep this terminal running):

```bash
export NHB_VALIDATOR_PASS="devnet-passphrase"
export NHB_RPC_TOKEN="devnet-token"
RPC_URL="http://127.0.0.1:8081"
# Explicitly opt into autogenesis for this throwaway devnet instance.
GOFLAGS=-buildvcs=false bin/nhb-node --config ./devnet.toml --allow-autogenesis
```

The first boot creates `validator-devnet.keystore`, autogenerates a genesis block, and logs the validator address. You can also
set `NHB_ALLOW_AUTOGENESIS=1` or `AllowAutogenesis = true` in `devnet.toml` if you prefer environment or config-based overrides.

## 3. Bootstrap accounts & voting power

In a second terminal, export the RPC details for the CLI:

```bash
export RPC_URL="http://127.0.0.1:8081"
export NHB_RPC_TOKEN="devnet-token"
```

1. Generate three operator keys (one proposer + two voters) and capture their addresses:

   ```bash
   bin/nhb-cli generate-key > proposer.key
   bin/nhb-cli generate-key > voter1.key
   bin/nhb-cli generate-key > voter2.key
   proposer=$(bin/nhb-cli balance proposer.key | jq -r '.address')
   voter1=$(bin/nhb-cli balance voter1.key | jq -r '.address')
   voter2=$(bin/nhb-cli balance voter2.key | jq -r '.address')
   ```

2. Seed the three accounts from the validator (replace `VALIDATOR_ADDR` with the address printed on node start):

   ```bash
   validator="VALIDATOR_ADDR"
   bin/nhb-cli send "$proposer" 500000000000000000 proposer.key --rpc $RPC_URL
   bin/nhb-cli send "$voter1" 500000000000000000 proposer.key --rpc $RPC_URL
   bin/nhb-cli send "$voter2" 500000000000000000 proposer.key --rpc $RPC_URL
   ```

3. Give the voters stake so they appear in the next POTSO snapshot:

   ```bash
   bin/nhb-cli stake 100 proposer.key
   bin/nhb-cli stake 100 voter1.key
   bin/nhb-cli stake 100 voter2.key
   # Trigger a heartbeat to accrue engagement
   bin/nhb-cli heartbeat proposer.key
   bin/nhb-cli heartbeat voter1.key
   bin/nhb-cli heartbeat voter2.key
   ```

Wait ~1 minute for the 1-block POTSO epoch to close; the node log prints `potso.reward.ready` when the snapshot is committed.

## 4. Proposal lifecycle

### 4.1 Craft payload

`payload.json` defines the desired parameter delta:

```bash
cat > payload.json <<'JSON'
{
  "fees.baseFee": "2000000000"
}
JSON
```

### 4.2 Propose

Lock a 0.2 ZNHB deposit and submit:

```bash
bin/nhb-cli gov propose \
  --kind param.update \
  --payload @payload.json \
  --from "$proposer" \
  --deposit 200000000000000000
```

The CLI prints `{"proposalId":1,...}`. Confirm the record:

```bash
bin/nhb-cli gov show --id 1 | jq
```

### 4.3 Vote

Cast ballots from each voter (choices are case-insensitive):

```bash
bin/nhb-cli gov vote --id 1 --from "$proposer" --choice yes
bin/nhb-cli gov vote --id 1 --from "$voter1" --choice yes
bin/nhb-cli gov vote --id 1 --from "$voter2" --choice abstain
```

### 4.4 Finalize after the voting window

The vote period is 120 seconds. Once elapsed:

```bash
sleep 130
bin/nhb-cli gov finalize --id 1 | jq
```

The response includes a tally (`yes_ratio_bps`, `turnout_bps`) and the proposal status should read `"passed"`.

### 4.5 Queue & execute

Queue the proposal (this records `queued=true` and sets the timelock):

```bash
bin/nhb-cli gov queue --id 1
```

After the 60 second timelock, execute and apply the parameter update:

```bash
sleep 65
bin/nhb-cli gov execute --id 1
```

### 4.6 Verify the parameter diff

Query the parameter store directly:

```bash
curl -s "$RPC_URL" -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{"jsonrpc":"2.0","id":1,"method":"gov_proposal","params":[{"id":1}]}' | jq '.result.status'

curl -s "$RPC_URL" -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{"jsonrpc":"2.0","id":1,"method":"nhb_paramGet","params":["fees.baseFee"]}' | jq -r '.result.value'
```

The first command shows `"executed"`; the second returns `"2000000000"`, confirming the live parameter now reflects the proposal payload.

## 5. Cleanup / rerun

Stop the node (`Ctrl+C`), remove state (`rm -rf nhb-data-devnet validator-devnet.keystore payload.json *.key`), and restart from step 2 for another demo.

---

Following the commands above—without modification—spins a deterministic governance devnet, completes a vote with multiple participants, and validates the resulting on-chain parameter change.
