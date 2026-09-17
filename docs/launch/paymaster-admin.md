# Paymaster Sponsorship Administration

This guide documents how the NHB genesis operator ("master key") can manage the paymaster sponsorship module during network launch and outlines the path to migrate control to on-chain governance.

The paymaster module enables gas sponsorship for end-user transactions. When active, transactions that include a populated `paymaster` payload and valid sponsor signature will have their execution fees debited from the sponsor account rather than the sender. Sponsors must pre-fund the required gas budget; any unused allowance is automatically refunded after execution.

## 1. Genesis Configuration and Roles

* The canonical NHB chain ID is `0x4e4842` (ASCII `"NHB"`). All administration calls must target this chain ID to be accepted by the node.
* The genesis spec should assign the network owner (master key) to the `ROLE_PAYMASTER_ADMIN` role under the `roles` section. Example snippet:

```json
{
  "roles": {
    "ROLE_PAYMASTER_ADMIN": ["nhb1masterkeyaddress…"]
  }
}
```

* Only addresses with `ROLE_PAYMASTER_ADMIN` may toggle the module via RPC. Additional administrators can be added later through role management transactions or governance proposals.

## 2. Inspecting Module Status

Use the unauthenticated `tx_getSponsorshipConfig` JSON-RPC method to confirm the module status:

```bash
curl -s \
  -X POST http://127.0.0.1:8545 \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tx_getSponsorshipConfig","params":[]}'
```

Example response:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "enabled": true,
    "adminRole": "ROLE_PAYMASTER_ADMIN"
  }
}
```

The `enabled` flag reflects the node's current sponsorship mode. This call is read-only and does not require the admin bearer token.

## 3. Enabling or Disabling Sponsorship

Administrative changes require the RPC bearer token (`NHB_RPC_TOKEN`) and must include the caller address assigned to `ROLE_PAYMASTER_ADMIN`.

```bash
curl -s \
  -X POST http://127.0.0.1:8545 \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $NHB_RPC_TOKEN" \
  -d '{
        "jsonrpc":"2.0",
        "id":2,
        "method":"tx_setSponsorshipEnabled",
        "params":[{"caller":"nhb1masterkeyaddress…","enabled":false}]
      }'
```

Set `enabled` to `true` to turn sponsorship back on. A successful call returns the updated configuration object. If the caller lacks the role, the node responds with HTTP `403` and message `"paymaster: caller lacks ROLE_PAYMASTER_ADMIN"`.

## 4. Previewing Sponsorship Diagnostics

Clients can preflight a transaction to understand sponsorship viability using `tx_previewSponsorship` (no authentication required). Submit the signed transaction payload (matching chain ID `0x4e4842`) and review the response:

```bash
curl -s \
  -X POST http://127.0.0.1:8545 \
  -H 'Content-Type: application/json' \
  -d '{
        "jsonrpc":"2.0",
        "id":3,
        "method":"tx_previewSponsorship",
        "params":[{"chainId":"0x4e4842","gasLimit":21000,"gasPrice":"1000000000","nonce":5,"paymaster":"0x…","paymasterR":"…","paymasterS":"…","paymasterV":"…","r":"…","s":"…","v":"…"}]
      }'
```

Response fields:

* `status`: One of `ready`, `module_disabled`, `signature_missing`, `signature_invalid`, `insufficient_balance`, or `none`. A `status` of `none` now carries the reason "transaction does not request sponsorship," distinguishing unsponsored submissions from malformed paymaster payloads.
* `reason`: Human-readable explanation when the status is not `ready`.
* `requiredBudgetWei`: Gas limit × price budget expected from the sponsor.
* `sponsor`: Address that would be charged for gas if sponsorship proceeds.
* `gasPriceWei`: Effective gas price the sponsor is expected to cover.
* `willRevert`: Boolean flag set to `true` when the request cannot reach `ApplyMessage`—specifically when `status` is `module_disabled`, `signature_missing`, `signature_invalid`, or `insufficient_balance`.
* `moduleEnabled`: Echoes the current global toggle.

When `willRevert` is `true`, the node aborts before `ApplyMessage`, meaning the transaction never reaches execution and no sponsor fallback occurs. Integrators must treat these cases as hard failures instead of attempting to fall back to sender-funded gas.

Clients should only broadcast transactions with `status == "ready"` to avoid fallback gas charges to the sender.

## 5. Operational Playbook

1. **Launch phase (network-sponsored):** Leave the module enabled (default). The master key monitors paymaster balances, tops up sponsor accounts, and uses `tx_previewSponsorship` to diagnose any user support cases.
2. **Transition phase:** Disable the module (`enabled: false`) once users are educated on self-funded transactions. All future transactions will have gas debited from senders automatically, while historical sponsorship events remain auditable via `tx.sponsorship.*` events.
3. **Governance handover:** Draft a governance proposal that grants `ROLE_PAYMASTER_ADMIN` to the governance executor (e.g., treasury multi-sig) and removes the master key from the role set. Future toggles then require a proposal vote instead of direct RPC calls.

## 6. Event Audit Trail

When the module processes transactions it emits structured events that downstream systems or indexers can consume:

| Event Type | Description | Key Attributes |
|------------|-------------|----------------|
| `tx.sponsorship.applied` | Sponsor successfully covered gas. | `txHash`, `sender`, `sponsor`, `gasUsed`, `chargedWei`, `refundWei`. |
| `tx.sponsorship.failed` | Sponsorship rejected; sender paid gas. | `txHash`, `sender`, `sponsor`, `status`, `reason`. |

These events are accessible via the node's event stream and help support teams reconcile gas reimbursements and failures.

## 7. Future Governance Integration

To migrate control to governance:

1. Submit a governance proposal that assigns `ROLE_PAYMASTER_ADMIN` to the designated governance executor address and, optionally, removes the master key role member.
2. Upon proposal execution, administrators should verify `tx_getSponsorshipConfig` to confirm the new role holder.
3. The governance engine can then encode calls to `tx_setSponsorshipEnabled` in future proposals, allowing on-chain votes to enable or disable network sponsorship.

By following this playbook, the chain owner can safely operate the paymaster module during the onboarding phase and gradually transition control to decentralised governance.
