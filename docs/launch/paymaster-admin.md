# Paymaster Sponsorship Administration

This guide documents the paymaster (gas sponsorship) module as the code implements it:
the role name, the two live RPC methods (and the retired third), the sponsorship statuses
and the events. Sources: `core/sponsorship.go`, `core/node.go`, `rpc/modules/transactions.go`,
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
* The role name `ROLE_PAYMASTER_ADMIN` is what `tx_getSponsorshipConfig` reports as `adminRole`
  (`rpc/modules/transactions.go`). It no longer gates any RPC method: the only method that did,
  `tx_setSponsorshipEnabled`, is retired (see section 3), and the module's enabled flag has no
  runtime setter (`core/node.go`). The one place the code still uses this role is as the built-in
  default `ApproverRole` of the automatic top-up policy
  (`[global.Paymaster.AutoTopUp.Governance] ApproverRole`, `config/config.go`; see
  [auto top-up](../runbooks/paymaster-autotopup.md)). Roles are assigned in the
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
the node's `RPCAddress` (the sample `config.toml` uses `127.0.0.1:8545`). A node starts with the
module enabled (`core/state_transition.go`, `NewStateProcessor`).

## 3. Enabling and disabling sponsorship

The module cannot be switched off while a node runs. `tx_setSponsorshipEnabled` is retired:
whatever the request or credential, the server answers HTTP 410 with error code `-32060`
(`handleTxSetSponsorshipEnabled`, `rpc/http.go`); its handler and `SetPaymasterModuleEnabled`
were removed. The method took a `caller` address from the request body without any proof that
the requester held that key, and it flipped a flag in the memory of the one node that received
it; because that flag decides whether a sponsored transaction is valid at all, different
validators could disagree on which blocks were acceptable. Nothing in the configuration file
sets the flag either, so sponsorship stays enabled on every node (`enabled` is always `true`,
and the status `module_disabled` below is not reachable on a running node), and
`tx_getSponsorshipConfig` (section 2) and `tx_previewSponsorship` (section 4) remain live. To
stop sponsorship from being used, use the limits in
[paymaster budgets](../runbooks/paymaster-budgets.md).

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

* `status`: `none`, `module_disabled` (defined in the code; not reachable on a running node, see
  section 3), `signature_missing`, `signature_invalid`, `insufficient_balance`, `throttled` or
  `ready`.
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
* `moduleEnabled`: whether the module is enabled on the node answering (always `true` on a running node).
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
