# Example applications

This directory documents the code under [`/examples`](../../examples). Every statement here was checked against the source in this repository; the source is the reference if the two ever disagree.

## Read this first: several examples call retired RPC methods

The node's RPC layer disables a set of methods. They return HTTP `410 Gone` with error code `codeMethodDisabled` (`-32060`, defined in `rpc/http.go`). Most of them used to mutate validator-local state outside the block pipeline; the exceptions are noted in the table. Disabled methods, by handler file:

| Family | Disabled methods | Source |
| --- | --- | --- |
| `creator_*` | `creator_publish`, `creator_tip`, `creator_stake`, `creator_unstake`, and `creator_payouts` with `claim: true` | `rpc/creator_handlers.go` |
| `identity_*` | `identity_setAlias`, `identity_setAvatar`, `identity_addAddress`, `identity_removeAddress`, `identity_setPrimary`, `identity_rename`, `identity_createClaimable`, `identity_claim` | `rpc/identity_handlers.go` |
| `claimable_*` | `claimable_create`, `claimable_claim`, `claimable_cancel` | `rpc/claimable_handlers.go` |
| `escrow_*` | `escrow_create`, `escrow_fund`, `escrow_release`, `escrow_refund`, `escrow_dispute`, `escrow_expire`, `escrow_resolve`, and the milestone writes `escrow_milestoneCreate`, `escrow_milestoneFund`, `escrow_milestoneRelease`, `escrow_milestoneCancel`, `escrow_milestoneSubscriptionUpdate` (`escrow_get`, `escrow_getRealm`, `escrow_getSnapshot`, `escrow_listEvents` and `escrow_milestoneGet` are live; `escrow_milestoneGet` requires a bearer token) | `rpc/escrow_handlers.go`, `rpc/escrow_milestone_handlers.go` |
| `p2p_*` | `p2p_createTrade`, `p2p_settle`, `p2p_dispute`, `p2p_resolve` (`p2p_getTrade` is live) | `rpc/p2p_handlers.go` |
| `lending_*` | `lending_supplyNHB`, `lending_withdrawNHB`, `lending_depositZNHB`, `lending_withdrawZNHB`, `lending_borrowNHB`, `lending_borrowNHBWithFee`, `lending_repayNHB`, `lending_liquidate` | `rpc/lending_handlers.go` |
| `stake_*` | `stake_delegate`, `stake_undelegate`, `stake_claim`, `stake_claimRewards` | `rpc/stake_handlers.go` |
| `loyalty_*` | `loyalty_createBusiness`, `loyalty_setPaymaster`, `loyalty_addMerchant`, `loyalty_removeMerchant`, `loyalty_createProgram`, `loyalty_updateProgram`, `loyalty_pauseProgram`, `loyalty_resumeProgram` | `rpc/loyalty_handlers.go` |
| `reputation_*` | `reputation_verifySkill` | `rpc/reputation_handlers.go` |
| `pos_*` | `pos_sweepVoids` (expired authorizations are voided by every block) | `rpc/http.go` (`posSweepRPCDisabledMessage`) |
| `potso_*` | `potso_reward_claim` | `rpc/potso_reward_handlers.go` |
| `sync_*` | `sync_snapshot_export`, `sync_snapshot_import` (`sync_status` is live) | `rpc/sync_handlers.go` |
| `tx_*` | `tx_setSponsorshipEnabled` (`tx_getSponsorshipConfig` and `tx_previewSponsorship` are live) | `rpc/http.go` (`sponsorshipToggleRPCDisabledMessage`) |

The lending and stake error messages (`rpc/lending_handlers.go`, `rpc/stake_handlers.go`) tell callers to submit the equivalent signed transaction through `nhb_sendTransaction`. The loyalty message (`rpc/loyalty_handlers.go`) tells callers to submit the equivalent transaction of the matching `TxType`, signed by the caller's own key, without naming a method. The escrow (including milestone), creator, identity, claimable, p2p and reputation messages say a signed-transaction replacement is pending. The other four messages differ: `pos_sweepVoids` (every block voids expired authorizations), `potso_reward_claim` (rewards are paid at epoch end), `sync_snapshot_*` (a new validator is brought up from a verified snapshot outside the node, see [`docs/validators/snapshot-onboarding.md`](../validators/snapshot-onboarding.md)) and `tx_setSponsorshipEnabled` (sponsorship stays enabled on every node). There is no creator, escrow-milestone or reputation transaction type in `core/types/transaction.go`, so those operations have no live write path.

## The examples

| Example | Guide | Status against the current node |
| --- | --- | --- |
| Workspace, shared SDK, status dashboard, network monitor | [overview.md](overview.md) | Runs. The dashboard's `/rpc-status` route calls an RPC method named `status` that the node does not have. |
| RPC cookbook scripts | [cookbook.md](cookbook.md) | The `nhb_getBalance` and `nhb_getLatestTransactions` calls work. The REST leg calls routes that do not exist in `services/escrow-gateway`. |
| Wallet gateway submission | [wallets.md](wallets.md) | Route exists in `gateway/routes/transactions.go`. |
| Wallet Lite | [wallet-lite.md](wallet-lite.md) | Read paths work (`nhb_getBalance`, `identity_resolve`). Alias, claimable, claim and creator flows hit retired methods. |
| P2P Mini-Market | [p2p-mini-market.md](p2p-mini-market.md) | Read paths work. Every write route returns `410` locally. |
| Creator Studio | [creator-studio.md](creator-studio.md) | Only the payout ledger read works. |
| Escrow checkout widget and merchant demo | [escrow-checkout.md](escrow-checkout.md) | Talks to a REST API whose routes are not implemented in this repository. |
| Freelance board | [freelance-board.md](freelance-board.md) | Static UI, makes no RPC calls. The RPC methods it names are retired. |
| Merchant Loyalty Console | [`examples/merchant-loyalty-console/README.md`](../../examples/merchant-loyalty-console/README.md) | Reads work. Writes hit retired methods. Fan-rewards calls use methods the node does not have. |
| Lending dApp | [`examples/lending-dapp/README.md`](../../examples/lending-dapp/README.md) | Mock data only, no network calls. |

## Requirements

- Yarn v1 for the workspace (`packageManager` is `yarn@1.22.19` in `examples/package.json`) and Node.js with a global `fetch` (the cookbook script and the lib-sdk client rely on it).
- Go, at the version in the root `go.mod` (`go 1.24.0`), for the Go programs. They live in the root module `nhbchain`, so run them from the repository root, for example `go run ./examples/cookbook/go`.

## Governance payload example

[`gov/param-update-proposal.json`](gov/param-update-proposal.json) is a `param.update` payload that sets `staking.minimumValidatorStake` to `1500000000000000000000` (1,500 ZNHB at 18 decimals). That parameter is the stake an account needs to be a validator candidate; while it is unset the default is 10,000 ZNHB (`defaultMinimumValidatorStakeWei` in `native/governance/types.go`), so this example lowers it. The value may be a bare number or a quoted decimal string; it must be a positive integer (`native/governance/engine.go`, `ParamKeyMinimumValidatorStake` validator; `MinimumValidatorStakeFromParam` in `native/governance/types.go`). `nhb-cli gov propose` takes the file as `--payload` (JSON, or `@path` to a file):

```bash
nhb-cli --rpc "$NHB_RPC_URL" gov propose \
  --kind param.update \
  --payload @docs/examples/gov/param-update-proposal.json \
  --key ./proposer.key \
  --deposit 1000e18
```

`nhb-cli` sends to `--rpc` (accepted anywhere on the command line) or the `RPC_URL` environment variable, and otherwise to `http://localhost:8080` (`defaultRPCEndpoint` in `cmd/nhb-cli/main.go`), which is not the checked-in `RPCAddress`. Flags are defined in `cmd/nhb-cli/gov.go` (`runGovPropose`): `--kind`, `--payload`, `--key` are required; `--deposit` defaults to `0` and accepts wei or the `1000e18` shorthand; the governance policy rejects any deposit below `[governance] MinDepositWei` with `governance: deposit below minimum` (`native/governance/engine.go`). That value is `1000e18` in the checked-in `config.toml` and is the default applied by `config/config.go` when the setting is empty, so the example above uses exactly the minimum; a node configured with a different `MinDepositWei` needs a different `--deposit`. The command signs a `TxTypeGovPropose` (`0x27`) transaction and sends it with `nhb_sendTransaction`, so `NHB_RPC_TOKEN` must be set (see `rpcAuthToken` in `cmd/nhb-cli/main.go`; `nhb-cli rpc-token`, run on the node host with the node's JWT secret in `NHB_RPC_JWT_SECRET` or on stdin with `--secret-stdin`, prints a token for it, valid 10 minutes by default and at most 24 hours; `cmd/nhb-cli/rpc_token.go`). A `param.update` payload may only contain keys in the node's governance allow-list (`AllowedParams` in `config.toml`; enforced in `native/governance/engine.go`, `validateParamPayload`).

## Identity HTTP examples

[`identity/`](identity) holds `.http` request files:

- `resolve.http` calls `identity_resolve` (live).
- `email-verify.http` calls the identity gateway's `/identity/email/register`, `/identity/email/verify` and `/identity/alias/bind-email` (`services/identity-gateway/server.go`).

The four `.http` files for the retired write methods (`identity_setAlias`, `identity_addAddress`, `identity_createClaimable`, `identity_claim`) were removed.

## Operator helpers

`examples/docs/ops` holds Go programs that read node state directly. They are documented in [`examples/README.md`](../../examples/README.md#operator-tooling).
