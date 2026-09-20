# Paymaster Sponsorship Administration

This guide documents the paymaster (gas sponsorship) module as the code implements it:
the role that may toggle it, the three RPC methods, the sponsorship statuses and the events.
Sources: `core/sponsorship.go`, `core/node.go`, `rpc/modules/transactions.go`,
`rpc/http.go`, `core/events/sponsorship.go`.

A transaction requests sponsorship by carrying a `paymaster` address together with a
paymaster signature (`paymasterR`, `paymasterS`, `paymasterV`). The pre-flight evaluation
(`EvaluateSponsorship`) requires the sponsor to hold at least `gasLimit * gasPrice` in NHB
(status `insufficient_balance` otherwise). When an NHB transfer is applied with an accepted
sponsor, the transfer fee that the sender would otherwise pay (`TransferGasPolicy.ComputeFee`,
see [fees and throttles](../runbooks/fees-and-throttles.md)) is debited from the sponsor's NHB
balance instead, and the sponsor must hold that amount too. A sponsored transaction whose
status is neither `ready` nor `none` is rejected with
`transaction sponsorship rejected: status=<status> reason=<reason>` and a
`tx.sponsorship.failed` event; the sender does not pay instead (`core/state_transition.go`).

## 1. Chain ID and roles

* The chain ID of every transaction is `0x4e4842` (decimal `5130306`, ASCII `NHB`)
  (`core/types/transaction.go`).
* The role that may toggle the module is `ROLE_PAYMASTER_ADMIN`. Roles are assigned in the
  `roles` object of the genesis file, which maps a role name to a list of bech32 addresses
  (`core/genesis/spec.go`):

```json
{
  "roles": {
    "ROLE_PAYMASTER_ADMIN": ["nhb1..."]
  }
}
```

* A governance `role.allowlist` proposal can also grant or revoke a role with the payload
  `{"grant": [{"role": "...", "address": "..."}], "revoke": [...]}`, but only for roles listed
  in `Governance.AllowedRoles` of the node's `config.toml`
  (`parseRoleAllowlistPayload` in `native/governance/engine.go`). The sample `config.toml`
  in this repository lists `MINTER_NHB`, `ROLE_SWAP_PAYOUT_ATTESTOR`,
  `ROLE_ESCROW_REALM_ADMIN` and `ROLE_LOYALTY_ADMIN`, not `ROLE_PAYMASTER_ADMIN`.

## 2. Inspect the module status

`tx_getSponsorshipConfig` takes no parameters and needs no authentication:

```bash
curl -s -X POST <rpc-endpoint> -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tx_getSponsorshipConfig","params":[]}'
```

The result is `{"enabled": true, "adminRole": "ROLE_PAYMASTER_ADMIN"}`. `<rpc-endpoint>` is
the node's `RPCAddress` (the sample `config.toml` uses `127.0.0.1:8545`). A fresh node starts
with the module enabled (`core/state_transition.go`).

## 3. Enable or disable sponsorship

`tx_setSponsorshipEnabled` requires RPC authentication (a bearer token) and one parameter
object:

```bash
curl -s -X POST <rpc-endpoint> -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tx_setSponsorshipEnabled",
       "params":[{"caller":"<bech32 address holding ROLE_PAYMASTER_ADMIN>","enabled":false}]}'
```

`NHB_RPC_TOKEN` is the variable `nhb-cli` reads for the token. On success the result is the
same object as `tx_getSponsorshipConfig`. If `caller` does not hold the role the response is
HTTP 403 with the message `paymaster: caller lacks ROLE_PAYMASTER_ADMIN`. A missing `caller`
returns `caller required`.

Behaviour to be aware of (`SetPaymasterModuleEnabled` in `core/node.go`):

* The call changes a flag in the memory of the node that received it. It is not a
  transaction and nothing is written to chain state, so it does not reach other nodes and it
  is not kept across a restart (a restarted node is enabled again).
* `caller` is a plain address string in the request. It is checked against the role, but no
  signature proves that the requester controls that address. Access control therefore rests on
  the RPC bearer token.

## 4. Preview sponsorship

`tx_previewSponsorship` needs no authentication. Its one parameter is a transaction object in
the JSON form of `core/types.Transaction`: `chainId`, `type`, `nonce`, `to`, `value`, `data`,
`gasLimit`, `gasPrice`, `maxBlockHeight`, `paymaster`, `intentRef`, `intentExpiry`, `merchantAddr`,
`deviceId`, `refundOf`, `r`, `s`, `v`, `paymasterR`, `paymasterS`, `paymasterV`. There is no
custom JSON codec, so the standard Go rules apply: `chainId`, `value`, `gasPrice` and the
signature values are JSON numbers, and the byte fields `to`, `data`, `paymaster` and
`intentRef` are base64 strings. `maxBlockHeight`, `paymaster`, `intentRef`, `intentExpiry`,
`merchantAddr`, `deviceId`, `refundOf` and the three paymaster signature values are omitted
when empty (`omitempty` tags in `core/types/transaction.go`).

The result fields (`SponsorshipPreviewResult`) are:

* `status`: `none`, `module_disabled`, `signature_missing`, `signature_invalid`,
  `insufficient_balance`, `throttled` or `ready`.
* `reason`: text for every status except `none` and `ready`. Values in the code:
  `paymaster address cannot be zero`, `paymaster module disabled`,
  `missing paymaster signature`, `invalid paymaster signature`,
  `unable to recover paymaster`, `paymaster balance below required gas budget`,
  `merchant sponsorship paused`, `device sponsorship revoked`,
  `device registered to merchant <address>`, `global sponsorship cap reached`,
  `merchant sponsorship cap reached`, `merchant address required for sponsorship throttling`,
  `device identifier required for sponsorship throttling` and
  `device sponsorship cap reached`.
* `sponsor`: the paymaster address (bech32).
* `gasPriceWei` and `requiredBudgetWei` (`gasLimit * gasPrice`).
* `moduleEnabled`: whether the module is enabled on the node answering.
* `throttle`, only for a throttled result: `scope`, `merchant`, `deviceId`, `day`, `limitWei`,
  `usedBudgetWei`, `attemptBudgetWei`, `txCount` and `limitTxCount`, each only when set.

`status` is `none` when the transaction has no `paymaster`. Only `ready` means the checks pass;
the merchant, device and cap checks behind `throttled` are described in
[paymaster budgets](../runbooks/paymaster-budgets.md).

## 5. Events

| Event type | Meaning | Attributes |
| --- | --- | --- |
| `tx.sponsorship.applied` | A sponsor covered the gas. | `txHash`, `gasUsed`, `sender`, `sponsor`, `gasPriceWei`, `chargedWei`, `refundWei` |
| `tx.sponsorship.failed` | Sponsorship was rejected. | `txHash`, `status`, `reason`, `sender`, `sponsor` |
| `paymaster.throttled` | A daily cap (global, merchant or device) blocked sponsorship. It is not emitted for POS-registry rejections (paused merchant, revoked device, device bound to another merchant), which produce only `tx.sponsorship.failed`. | `scope`, `txHash`, `merchant`, `deviceId`, `day`, `limitWei`, `usedBudgetWei`, `attemptBudgetWei`, `txCount`, `limitTxCount` |
| `paymaster.autotopup` | The automatic top-up ran. | see [auto top-up](../runbooks/paymaster-autotopup.md) |

Optional attributes appear only when they have a value.
