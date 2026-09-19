# Example applications

This directory documents the code under [`/examples`](../../examples). Every statement here was checked against the source in this repository; the source is the reference if the two ever disagree.

## Read this first: several examples call retired RPC methods

The node's RPC layer permanently disables a set of write methods. They return HTTP `410 Gone` with error code `codeMethodDisabled` (defined in `rpc/http.go`), because they used to mutate validator-local state outside the block pipeline. Disabled methods, by handler file:

| Family | Disabled methods | Source |
| --- | --- | --- |
| `creator_*` | `creator_publish`, `creator_tip`, `creator_stake`, `creator_unstake`, and `creator_payouts` with `claim: true` | `rpc/creator_handlers.go` |
| `identity_*` | `identity_setAlias`, `identity_setAvatar`, `identity_addAddress`, `identity_removeAddress`, `identity_setPrimary`, `identity_rename`, `identity_createClaimable`, `identity_claim` | `rpc/identity_handlers.go` |
| `claimable_*` | `claimable_create`, `claimable_claim`, `claimable_cancel` | `rpc/claimable_handlers.go` |
| `escrow_*` | `escrow_create`, `escrow_fund`, `escrow_release`, `escrow_refund`, `escrow_dispute`, `escrow_expire`, `escrow_resolve` (`escrow_get`, `escrow_getRealm`, `escrow_getSnapshot`, `escrow_listEvents` and the `escrow_milestone*` methods are not disabled, but the milestone write methods have the same defect, see below) | `rpc/escrow_handlers.go` |
| `p2p_*` | `p2p_createTrade`, `p2p_settle`, `p2p_dispute`, `p2p_resolve` (`p2p_getTrade` is live) | `rpc/p2p_handlers.go` |
| `lending_*` | `lending_supplyNHB`, `lending_withdrawNHB`, `lending_depositZNHB`, `lending_withdrawZNHB`, `lending_borrowNHB`, `lending_borrowNHBWithFee`, `lending_repayNHB`, `lending_liquidate` | `rpc/lending_handlers.go` |
| `stake_*` | `stake_delegate`, `stake_undelegate`, `stake_claim`, `stake_claimRewards` | `rpc/stake_handlers.go` |
| `loyalty_*` | `loyalty_createBusiness`, `loyalty_setPaymaster`, `loyalty_addMerchant`, `loyalty_removeMerchant`, `loyalty_createProgram`, `loyalty_updateProgram`, `loyalty_pauseProgram`, `loyalty_resumeProgram` | `rpc/loyalty_handlers.go` |

The lending and stake error messages (`rpc/lending_handlers.go`, `rpc/stake_handlers.go`) tell callers to submit the equivalent signed transaction through `nhb_sendTransaction`. The loyalty message (`rpc/loyalty_handlers.go`) tells callers to submit the equivalent transaction of the matching `TxType`, signed by the caller's own key, without naming a method. The escrow, creator, identity, claimable and p2p messages say a signed-transaction replacement is pending. There is no creator transaction type in `core/types/transaction.go`.

Caution: the `escrow_milestone*` write methods (`escrow_milestoneCreate`, `Fund`, `Release`, `Cancel`, `SubscriptionUpdate`) and `reputation_verifySkill` are not disabled, but the node methods behind them (`core/node.go`: `EscrowMilestoneCreate`, `EscrowMilestoneFund`, `EscrowMilestoneRelease`, `EscrowMilestoneCancel`, `EscrowMilestoneSubscriptionUpdate`, `ReputationVerifySkill`) take `n.stateMu` and write `n.state.Trie` directly, outside the block pipeline. That is the same pattern that the comment on `escrowRPCDisabledMessage` in `rpc/escrow_handlers.go` describes as the reason the other methods were retired (a validator-local state change that other validators do not reproduce, leading to a state-root mismatch on the next block). Do not call them against a multi-validator network.

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
| Freelance board | [freelance-board.md](freelance-board.md) | Static UI, makes no RPC calls. |
| Merchant Loyalty Console | [`examples/merchant-loyalty-console/README.md`](../../examples/merchant-loyalty-console/README.md) | Reads work. Writes hit retired methods. Fan-rewards calls use methods the node does not have. |
| Lending dApp | [`examples/lending-dapp/README.md`](../../examples/lending-dapp/README.md) | Mock data only, no network calls. |

## Two kinds of examples

- Yarn v1 for the workspace (`packageManager` is `yarn@1.22.19` in `examples/package.json`) and Node.js with a global `fetch` (the cookbook script and the lib-sdk client rely on it).
- Go, at the version in the root `go.mod` (`go 1.24.0`), for the Go programs. They live in the root module `nhbchain`, so run them from the repository root, for example `go run ./examples/cookbook/go`.

## Governance payload example

[`gov/param-update-proposal.json`](gov/param-update-proposal.json) is a `param.update` payload. `nhb-cli gov propose` takes it as `--payload` (JSON, or `@path` to a file):

```bash
nhb-cli gov propose \
  --kind param.update \
  --payload @docs/examples/gov/param-update-proposal.json \
  --key ./proposer.key \
  --deposit 1000e18
```

Flags are defined in `cmd/nhb-cli/gov.go` (`runGovPropose`): `--kind`, `--payload`, `--key` are required; `--deposit` defaults to `0` and accepts wei or the `1000e18` shorthand; the governance policy rejects any deposit below `[governance] MinDepositWei` with `governance: deposit below minimum` (`native/governance/engine.go`). That value is `1000e18` in the checked-in `config.toml` and is the default applied by `config/config.go` when the setting is empty, so the example above uses exactly the minimum; a node configured with a different `MinDepositWei` needs a different `--deposit`. The command signs a `TxTypeGovPropose` (`0x27`) transaction and sends it with `nhb_sendTransaction`, so `NHB_RPC_TOKEN` must be set (see `rpcAuthToken` in `cmd/nhb-cli/main.go`). A `param.update` payload may only contain keys in the node's governance allow-list (`AllowedParams` in `config.toml`; enforced in `native/governance/engine.go`, `validateParamPayload`).

## Identity HTTP examples

[`identity/`](identity) holds `.http` request files:

- `resolve.http` calls `identity_resolve` (live).
- `email-verify.http` calls the identity gateway's `/identity/email/register`, `/identity/email/verify` and `/identity/alias/bind-email` (`services/identity-gateway/server.go`).

The four `.http` files for the retired write methods (`identity_setAlias`, `identity_addAddress`, `identity_createClaimable`, `identity_claim`) were removed.

## Operator helpers

`examples/docs/ops` holds Go programs that read node state directly. They are documented in [`examples/README.md`](../../examples/README.md#operator-tooling).
