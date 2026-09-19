# Governance Proposal Types

A proposal has a `target` (kind) string and a JSON payload string. The kinds
are the `ProposalKind*` constants in `native/governance/types.go`. Payload
validation runs in `Engine.SubmitProposal` when the proposal is submitted and
again in `Engine.Execute` when it is applied. Payload structs are decoded with
`json.Unmarshal`, so unknown fields are ignored. Any other `target` fails with
`governance: unsupported proposal kind`.

Amounts named `...Wei` in struct payloads are decimal strings (no sign,
decimal point or exponent; an empty string counts as `0`). Addresses are bech32
strings.

| Target | Payload |
| --- | --- |
| `param.update`, `param.emergency_override`, `param.update_fee_rate` | JSON object of `key: value` pairs. All three kinds run the same validation and the same execution code (`Engine.applyParamUpdates`); `param.emergency_override` has no shorter timelock. See [params](../governance/params.md) for keys and value rules. |
| `policy.slashing` | `{"enabled": bool, "maxPenaltyBps": uint, "windowSeconds": uint, "maxSlashWei": string, "evidenceTtlSeconds": uint, "notes"?: string}` |
| `role.allowlist` | `{"grant"?: [{"role": string, "address": bech32}], "revoke"?: [{"role": string, "address": bech32}], "memo"?: string}` |
| `treasury.directive` | `{"source": bech32, "transfers": [{"to": bech32, "amountWei": string, "kind"?: string, "memo"?: string}], "memo"?: string}` |
| `policy.swapPriceSigner` | `{"provider": string, "signerAddress"?: bech32, "revoke"?: bool, "memo"?: string}` |
| `policy.buybackParams` | `{"feeShareBps": uint, "discountBps": uint, "safetyMarginBps": uint, "memo"?: string}` |
| `policy.swapRiskParams` | `{"redeemPerTxMinWei": string, "redeemPerTxMaxWei": string, "redeemPerAddressDailyCapWei": string, "redeemPerAddressMonthlyCapWei": string, "memo"?: string}` |
| `policy.redemptionFeeParams` | `{"feeBps": uint, "feeFloorWei": string, "feeCapWei": string, "memo"?: string}` |
| `policy.lendingRateSchedule` | `{"schedule": [{"tenureDays": uint, "rateBps": uint}], "memo"?: string}` |
| `policy.lendingDepositRateSchedule` | Same shape as `policy.lendingRateSchedule`. |

## Validation rules

- **`param.*`**: payload must be a JSON object with at least one key; every key
  must be in `AllowedParams` and have a validator. A value that fails its
  validator rejects the whole proposal.
- **`policy.slashing`**: `windowSeconds` in `60`-`2592000` (30 days);
  `maxPenaltyBps <= 10000`; `evidenceTtlSeconds` non-zero, `>= windowSeconds`
  and `<= 7776000` (90 days); `maxSlashWei <= 9223372036854775807`. Execution
  writes the five `slashing.policy.*` keys.
- **`role.allowlist`**: rejected with `role allowlist proposals are disabled`
  when the node's `AllowedRoles` is empty. Every role in `grant` and `revoke`
  must be in `AllowedRoles`, addresses must decode, and at least one grant or
  revoke entry is required. `MINTER_ZNHB` can never be granted, even if it is
  listed in `AllowedRoles`.
- **`treasury.directive`**: rejected with `treasury directives are disabled`
  when `TreasuryAllowList` is empty. `source` must be in `TreasuryAllowList`,
  there must be at least one transfer, and every `amountWei` must be positive.
  Execution debits the source's ZNHB balance by the total and credits each
  recipient; it fails with `governance: treasury insufficient balance` when the
  source cannot cover the total. `kind` and `memo` are recorded only in the
  audit record.
- **`policy.swapPriceSigner`**: `provider` is trimmed and must be 1-64
  characters. Unless `revoke` is `true`, `signerAddress` must decode and must
  not be the zero address. Execution calls `SwapSetPriceSigner` or, on revoke,
  `SwapClearPriceSigner`; the registered address is what
  `PriceProofEngine.Verify` (`native/swap/engine.go`) checks price-proof
  signatures against for `TxTypeSwapVoucherMint`.
- **`policy.buybackParams`**: each field `<= 10000`. There is no field for the
  buyback reference-price signer quorum; that is fixed at genesis.
- **`policy.swapRiskParams`**: `redeemPerTxMaxWei`,
  `redeemPerAddressDailyCapWei` and `redeemPerAddressMonthlyCapWei` must be
  positive; `redeemPerTxMinWei` may be `0`. Ordering required: `min <= max <=
  dailyCap <= monthlyCap` (the `min <= max` check applies when `min > 0`).
- **`policy.redemptionFeeParams`**: `feeBps <= 10000`, `feeCapWei > 0`,
  `feeFloorWei <= feeCapWei`.
- **`policy.lendingRateSchedule` / `policy.lendingDepositRateSchedule`**: 1 to
  20 entries; `tenureDays` `1`-`3650`; `rateBps` `1`-`10000`; no duplicate
  `tenureDays`. The schedule replaces the previous one as a whole, and only
  affects loans (or deposits) issued afterwards.

## Examples

Parameter update (numbers unquoted, see [params](../governance/params.md#how-a-parameter-proposal-is-checked)):

```json
{"staking.aprBps": 1250}
```

Slashing policy:

```json
{"enabled": true, "maxPenaltyBps": 400, "windowSeconds": 600,
 "maxSlashWei": "2500", "evidenceTtlSeconds": 1200}
```

Role change:

```json
{"grant": [{"role": "ROLE_LOYALTY_ADMIN", "address": "<new operator bech32>"}],
 "revoke": [{"role": "ROLE_LOYALTY_ADMIN", "address": "<old operator bech32>"}],
 "memo": "Rotate operators"}
```

Lending rate schedule:

```json
{"schedule": [{"tenureDays": 30, "rateBps": 1200}, {"tenureDays": 90, "rateBps": 1600}]}
```

Submit with `nhb-cli gov propose --kind <kind> --payload @payload.json --key <keyfile> --deposit 1000e18`
(see [governance API](../governance/api.md)).

## Related

Lifecycle and configuration: [overview](../governance/overview.md).
Service wrapper: [`governd`](./service.md). Policy checks:
[policy invariants](./policy-invariants.md).
