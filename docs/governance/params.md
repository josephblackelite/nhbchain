# Governance Parameters

> Include function-level documentation for developer integrations and technical specs; docs must be generated into /docs/governance/* for auditors, investors, regulators, and consumers.

The following parameters are governable by default. Governance payloads must
respect the validation guidance for each key; values outside the documented
range are rejected during execution.

| Key | Description | Validation Guidance |
| --- | --- | --- |
| `potso.weights.AlphaStakeBps` | Controls the proportion of POTSO rewards attributed to validator staking weight. | Integer basis points in the range `0`–`10,000`. Values above `10,000` would over-allocate rewards and are rejected. |
| `potso.rewards.EmissionPerEpochWei` | Defines the raw ZNHB emission allocated per epoch for POTSO incentives. | Unsigned integer encoded in Wei. Must be greater than or equal to `0` and should remain within 64-bit safe bounds (`< 9.22e18`) to avoid downstream overflow in accounting pipelines. |
| `fees.baseFee` | Sets the minimum base fee charged for network transactions. | Unsigned integer in Wei per gas. Must be non-negative; most deployments target a range between `0` and `1e15` (0.001 ZNHB) to keep fees affordable. |
