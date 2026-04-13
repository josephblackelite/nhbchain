# Governance Proposal Types

Governance payloads are executed by the on-chain governance engine. Each
proposal contains a `target` string that selects the execution pathway along
with a module-specific JSON payload. The constants below are defined in
`native/governance/types.go` and mirrored here for reference.

| Target | Description | Payload schema |
| ------ | ----------- | -------------- |
| `param.update` | Standard parameter updates executed after a successful vote and timelock. | JSON object containing the parameter key/value pairs to set. Keys must exist in the governance allow list. |
| `param.emergency_override` | Bypasses the standard timelock for critical parameter changes. | Same as `param.update`; execution occurs immediately after the proposal passes. |
| `policy.slashing` | Updates the slashing policy (enablement, penalties, evidence windows). | Matches `governance.SlashingPolicyPayload` with fields such as `enabled`, `maxPenaltyBps`, `windowSeconds`, and `evidenceTtlSeconds`. |
| `role.allowlist` | Grants or revokes governance-managed roles (e.g. treasury signers). | `{ "grant"?: [{"role": string, "address": bech32}], "revoke"?: [...] , "memo"?: string }`. Addresses are NHB bech32 strings. |
| `treasury.directive` | Disburses funds from a governance-controlled treasury bucket. | Array of transfers `{ "source": bech32, "transfers": [{"to": bech32, "amount": string, "kind": string, "memo"?: string}], "memo"?: string }`. Amounts are decimal strings in Wei. |

## General guidelines

* Payloads **must** be canonical JSON without additional whitespace to guarantee
a deterministic digest. Governance tooling should normalise numbers as base-10
strings and omit fields set to default values.
* Deposits are denominated in ZNHB and must be provided as positive integers.
* Targets outside the table above will be rejected during proposal submission.
* Emergency overrides should be limited to scenarios where rapid mitigation is
necessary; operators should still publish post-mortems describing the change.

Refer to `docs/governance/params.md` for the list of governable parameters and
to the service documentation for details on submitting proposals through
`governd`.
