# POTSO Configuration

POTSO is configured in the node's TOML file under `[potso.rewards]`, `[potso.weights]` and `[potso.abuse]` (`config.PotsoConfig` in `config/config.go`). `cmd/nhb/main.go` converts them with `Config.PotsoRewardConfig()` and `Config.PotsoWeightConfig()` and hands them to the node at start-up (`SetPotsoRewardConfig`, `SetPotsoWeightConfig`). Block execution reads them directly (`processPotsoRewardEpoch`), so they only change when the process is restarted with a different file. An invalid value makes start-up panic.

The repository's `config.toml` carries a full example. `config/prod.toml` has `[potso.rewards]` and `[potso.weights]` (with `TreasuryAddress = ""`) and no `[potso.abuse]` section.

## `[potso.rewards]`

| Key | Type | Description | Value if key is omitted |
| --- | --- | --- | --- |
| `EpochLengthBlocks` | uint | Blocks per reward epoch. `0` disables reward processing. | `0` |
| `AlphaStakeBps` | uint | Stake share in basis points. Used only if `[potso.weights].AlphaStakeBps` is `0`, which the loader prevents (see below). | `0` |
| `MinPayoutWei` | decimal string | Payouts below this many wei are dropped. | `"0"` |
| `EmissionPerEpoch` | decimal string | Maximum wei budget per epoch. | `"0"` |
| `TreasuryAddress` | bech32 (`nhb1...` or `znhb1...`) | Account that funds payouts. Required when rewards are enabled. | zero address |
| `MaxWinnersPerEpoch` | uint | Cap on winners per epoch. `0` means no cap. | `0` |
| `CarryRemainder` | bool | Parsed and stored in the config struct, but nothing reads it. | `true` |
| `PayoutMode` | string | `auto` or `claim` (case-insensitive; any other non-empty value is normalised to `auto` by `Normalise`). | `auto` |

Rewards are enabled only when `EpochLengthBlocks > 0` and `EmissionPerEpoch > 0` (`RewardConfig.Enabled`, `maybeProcessPotsoRewards`). `RewardConfig.Validate` also rejects: alpha or `MaxUserShareBps` above 10000, negative amounts, `EpochLengthBlocks > 0` with a non-positive emission, and an enabled config with a zero `TreasuryAddress`.

The TOML key is `EmissionPerEpoch`. The governance parameter name `potso.rewards.EmissionPerEpochWei` (see below) does not match it.

## `[potso.weights]`

| Key | Description | Default |
| --- | --- | --- |
| `AlphaStakeBps` | Stake share in basis points (0-10000). | `7000` |
| `TxWeightBps` | Transaction counter multiplier (max 10000). | `6000` |
| `EscrowWeightBps` | Escrow counter multiplier (max 10000). | `3000` |
| `UptimeWeightBps` | Uptime counter multiplier (max 10000). | `1000` |
| `MaxEngagementPerEpoch` | Cap applied to the EMA result. `0` in the runtime struct means no cap. | `1000` |
| `MinStakeToWinWei` | Minimum stake to stay in the snapshot (decimal string). | `"0"` |
| `MinEngagementToWin` | Minimum EMA to stay in the snapshot. | `0` |
| `DecayHalfLifeEpochs` | EMA half-life in epochs; at most `100000`. | `7` |
| `TopKWinners` | Maximum snapshot length. | `5000` |
| `TieBreak` | `addrHash` or `addrLex`. | `addrHash` |

## `[potso.abuse]`

| Key | Description | Default |
| --- | --- | --- |
| `MinStakeToEarnWei` | Stake below this forces engagement to 0 (decimal string). | `"0"` |
| `QuadraticTxDampenAfter` | Transaction count above which dampening starts. `0` disables it. | `0` |
| `QuadraticTxDampenPower` | Root exponent; must be `<= 1000`. Dampening applies only when `> 1`. | `2` |
| `MaxUserShareBps` | Maximum share of an epoch budget per winner, 0-10000. `0` means uncapped. | `0` |

`MinStakeToEarnWei` is read only from `[potso.abuse]`; there is no such key in `[potso.weights]`.

## Loader defaults

`config.Load` replaces a zero value with the default for these keys: `[potso.weights]` `AlphaStakeBps`, `TxWeightBps`, `EscrowWeightBps`, `UptimeWeightBps`, `MaxEngagementPerEpoch`, `DecayHalfLifeEpochs`, `TopKWinners`; `[potso.abuse]` `QuadraticTxDampenPower`; and empty strings for `MinPayoutWei` (`"0"`), `EmissionPerEpoch` (`"1000000000000000000"`, only when explicitly set to `""`), `MinStakeToWinWei`, `MinStakeToEarnWei`, `TieBreak`. Consequences:

- A file cannot set any of those numeric weight keys to `0`. For example `DecayHalfLifeEpochs = 0` becomes `7`, `TopKWinners = 0` becomes `5000` (so the cap cannot be turned off), `AlphaStakeBps = 0` becomes `7000`.
- Because `[potso.weights].AlphaStakeBps` is therefore always greater than `0` after loading, `[potso.rewards].AlphaStakeBps` is never the value used for rewards.
- `QuadraticTxDampenPower = 0` becomes `2`. `QuadraticTxDampenAfter` stays `0` (disabled) unless set.

## Governance parameters

`potso.weights.AlphaStakeBps`, `potso.rewards.EmissionPerEpochWei`, `potso.abuse.MaxUserShareBps`, `potso.abuse.MinStakeToEarnWei`, `potso.abuse.QuadraticTxDampenAfter` and `potso.abuse.QuadraticTxDampenPower` are in the default `AllowedParams` list (`config/config.go`, `config.toml`), and `native/governance/engine.go` (`validatorForParam`) validates values for them in a `param.update` proposal. No code outside that validator reads these keys from the parameter store, so executing such a proposal does not change POTSO behaviour. Only the TOML values above are used.

## `potso_params`

`potso_params` returns the running weight parameters (`Node.PotsoWeightConfig`): `alphaStakeBps`, `txWeightBps`, `escrowWeightBps`, `uptimeWeightBps`, `maxEngagementPerEpoch`, `minStakeToWinWei` (string), `minEngagementToWin`, `decayHalfLifeEpochs`, `topKWinners`, `tieBreak`. It does not return `MinStakeToEarnWei`, the dampening parameters, `MaxUserShareBps`, or any `[potso.rewards]` value. See [leaderboard.md](leaderboard.md).

## Effect on governance voting

Governance voting power is read from the latest processed epoch's weight snapshot (`CastVote` in `native/governance/engine.go`). With `EpochLengthBlocks = 0` or `EmissionPerEpoch = 0` no epoch is processed, so no snapshot exists and votes fail with "governance: potso snapshot unavailable".
