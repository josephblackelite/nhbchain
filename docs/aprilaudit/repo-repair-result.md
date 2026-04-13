# NHBChain Repo Repair Result

Date: 2026-04-12

## Scope Completed

This repair pass focused on the live-chain risk items, payment-core correctness, escrow compatibility, swap lifecycle completeness, Windows test stability, and repo-wide build/test integrity.

The codebase now passes:

```text
go test ./... -count=1
```

## Critical Protocol and Security Fixes Completed

### 1. Transaction signing domain tightened
- Native transaction hashing now binds `IntentRef` as well as `IntentExpiry`.
- This closes the signature-domain gap where a signed payment intent could be replayed against a different business/reference context.

### 2. Panic / DoS path removed from transaction hashing and submission
- Oversized `To` and `Paymaster` byte slices are now rejected during validation instead of panicking hash construction.
- RPC submission validates transactions before signature recovery.

### 3. RPC numeric parsing repaired
- Hex values are now parsed by prefix semantics instead of fragile alpha-hex detection.
- Large numeric transaction fields are parsed from raw JSON instead of lossy `float64` conversion.
- This stabilizes gas, value, nonce, and signature handling for standard clients.

### 4. NHB transfer accounting corrected
- Native NHB transfer handling now debits gas consistently instead of checking `value + gas` while only charging principal.
- Sponsored NHB transfers now correctly route gas to the paymaster path instead of bypassing sponsorship in the native fast path.

### 5. Hidden transfer-routing fee disabled
- The active hardcoded routing-tax path was removed from NHB and ZNHB transfer execution.
- Transfer events now align with actual delivered value instead of overstating receipt amounts.

### 6. Genesis safety restored
- Startup now rejects a genesis `chainId` that does not match the derived genesis hash.
- This restores a critical safeguard against accidental or unsafe chain bootstraps.

## Commerce and Feature-Path Fixes Completed

### 7. Heartbeat compatibility restored
- Heartbeat payload decoding now accepts JSON and RLP.
- This stabilizes quota / POTSO engagement flows across mixed payload producers.

### 8. Escrow payload compatibility restored
- Native `CreateEscrow` handling now accepts JSON and RLP payload encodings.
- This fixes broken escrow creation paths caused by encoding drift across the repo.

### 9. Escrow realm test/runtime alignment restored
- Realm metadata requirements are now consistently reflected in the escrow suite.
- The escrow engine, native escrow tests, and commerce flows are back in sync.

### 10. Swap cash-out abort path added
- Stable swap storage now supports aborting pending cash-out intents safely.
- Aborts preserve locked inventory rules and prevent accidental burn/completion on canceled intents.

## Repo and Platform Stability Fixes Completed

### 11. Paymaster auto-top-up path stabilized
- Native NHB sponsorship now triggers paymaster gas charging, optional auto-top-up, usage recording, and event emission consistently.
- Paymaster tests were normalized against the account-level funding ledger actually used by the engine.

### 12. Windows config test compatibility repaired
- TOML test fixtures now quote Windows filesystem paths correctly.

### 13. Graceful shutdown/resource cleanup added
- P2P server shutdown now closes listeners, peers, nonce guards, and background loops cleanly.
- Integration tests now close peerstores and temp DB resources deterministically.
- Swapd SQLite storage close is now idempotent and test helpers close stores automatically.

### 14. Root build collisions removed
- The ad hoc root helper scripts `generate_jwt.go` and `update_env.go` now carry `//go:build ignore` so they no longer break the top-level package build.

### 15. Secret hygiene improved
- Added ignores for:
  - `token.txt`
  - `KEYS & ENODE - SUNDAY 14-3.txt`
  - `cmd/nhb/nhb_startup_err.log`

## Runtime Surface Tightening Completed

### 16. Milestone escrow is now stateful, funded, and self-reconciling
- Milestone projects now persist in node state instead of stopping at wire-format validation.
- Funding moves value into deterministic per-leg vaults.
- Release settles from the vault to the payee.
- Cancellation refunds funded legs to the payer.
- Overdue funded legs now sweep to `expired` plus refund on later read/mutation.
- The milestone docs and example docs now match live execution semantics.

### 17. Explorer and operator RPCs now expose real data
- `nhb_getNetworkStats` now reports live validator count, derived epoch, mempool depth, and recent TPS instead of hardcoded placeholders.
- `nhb_getOwnerWalletStats` now reports the configured owner wallet, live NHB/ZNHB balances, and fee accrual aggregated from fee-routing events.
- `nhb_getSlashingEvents` now returns actual POTSO penalty events from the node event stream.

### 18. Staking preview output no longer uses placeholder math
- Balance/staking response paths now use the node’s real `StakePreviewClaim` estimator for pending staking rewards instead of mirroring `StakeShares` as a fake approximation.

### 19. POS gRPC responses now return real transaction hashes
- The POS gRPC submission layer now derives and returns the actual transaction hash from the signed envelope instead of returning the placeholder string `submitted`.

### 20. RPC network-unavailable handling normalized
- P2P and net RPC routes now use a shared explicit unavailable response instead of duplicated TODO-marked fallbacks.

### 21. No-op slasher semantics made safe and intentional
- The enabled no-op slasher no longer returns `not implemented`; it now behaves as an actual no-op success path for harnesses and non-state-backed environments.

### 22. Founder-style transfer gas policy is now explicit
- NHB transfers now support a first-class spend-based free-gas policy instead of relying on implicit behaviour.
- The threshold-crossing transfer remains free, and only later transfers start paying gas.
- Paid transfer gas now routes to the configured treasury collector instead of silently disappearing in the native transfer path.

### 23. Transfer sponsorship visibility is now queryable
- Added `fees_getTransferStatus` so wallets and dashboards can inspect a wallet's transfer spend, remaining free-tier headroom, and reset semantics.
- This makes the "fee-free until threshold" model observable and auditable.

### 24. `lendingd` is now backed by the real lending engine
- The `lendingd` daemon now connects to the node JSON-RPC lending surface through the existing lending engine adapter instead of starting as a pure `UNIMPLEMENTED` shell.
- The lending service docs were updated to match live behaviour.

### 25. `payoutd` now uses a real treasury wallet
- The payout daemon no longer starts with a dummy `treasury wallet not configured` stub.
- It now requires an explicit EVM treasury wallet configuration, validates signer/address/chain alignment at startup, and broadcasts real native or ERC-20 payout transactions for configured assets.
- Wallet confirmation polling remains part of the payout settlement path, so cash-out receipts are emitted only after the configured on-chain confirmations are observed.

### 26. Treasury routing is now visible through the payout admin surface
- `/status` now reports the active treasury wallet mode, chain ID, source address, and configured asset routes.
- This gives operators a direct reconciliation surface for verifying that USDC/USDT payout intents are wired to the intended hot wallet and token contracts before processing begins.

### 27. `payoutd` now enforces live hot-wallet balance safety
- Before broadcasting a stable payout, the processor now checks the hot wallet's on-chain balance for the requested asset.
- This closes the gap where policy soft inventory could still permit a payout that the treasury wallet could not actually settle.
- Underfunded payouts now fail fast with a clear treasury-balance error instead of reaching the broadcast leg and failing opaquely.

### 28. Treasury reconciliation and sweep guidance are now first-class
- Added `GET /treasury/reconcile` and `GET /treasury/sweep-plan` to `payoutd`.
- Operators can now inspect, per asset:
  - remaining daily cap
  - remaining tracked soft inventory
  - current on-chain hot-wallet balance
  - configured hot minimum / target thresholds
  - cold-wallet destination
  - recommended action (`none`, `refill_hot`, `sweep_to_cold`, `inspect_wallet`)
- This materially upgrades payoutd from "sender plus pause button" toward a true treasury control-plane service.

### 29. Treasury movement now has a persistent maker-checker workflow
- Added a persistent treasury instruction store in `payoutd` for refill and sweep actions.
- Operators can now:
  - create a treasury instruction
  - list pending/approved/rejected instructions
  - approve or reject an instruction with maker-checker enforcement
- Each instruction records amount, asset, source, destination, requester, reviewer, timestamps, and notes.
- This closes a major operational gap by turning treasury movement into an explicit, auditable workflow instead of an off-ledger manual note.

### 30. Mint-side reconciliation and export surfaces are now available
- `payments-gateway` no longer stops at quote creation, invoice creation, and per-invoice lookup.
- Added internal reconciliation endpoints for:
  - filtered invoice listing
  - status/amount summary aggregation
  - JSON / CSV export of invoice-to-mint settlement rows
- Export rows now join quote, invoice, NowPayments reference, mint status, and final transaction hash data, which makes the mint rail materially easier to reconcile with finance and partner systems.

### 31. Unified operator reporting is now available across mint and treasury
- Added a dedicated `ops-reporting` service that reads the live mint SQLite store and treasury instruction store.
- Operators can now query:
  - `GET /summary`
  - `GET /mint/invoices`
  - `GET /mint/export`
  - `GET /treasury/instructions`
  - `GET /treasury/export`
- The service is bearer-protected, read-only, and designed for finance, treasury, and operations teams that need one cross-rail surface for reconciliation and export.
- This closes the earlier gap where mint reporting and treasury workflow visibility existed, but were still fragmented across separate service surfaces.

### 32. `payments-gateway` now models the founder-grade inbound swap-mint rail
- Quote creation now separates:
  - the asset being minted on NHBChain
  - the external crypto the user is paying with through NOWPayments
- Quotes now expose:
  - `mintAsset`
  - `payCurrency`
  - `serviceFeeFiat`
  - `totalFiat`
  - `estimatedPayAmount`
- The NHB stablecoin path now supports the expected founder behaviour where a requested NHB mint amount maps 1:1 to USD quote value before optional fee uplift.
- Invoice creation now uses the chosen pay currency for NOWPayments while still minting NHB on successful settlement.

### 33. `payoutd` now persists payout execution outcomes
- Added a persistent payout execution store covering processing, settled, failed, and aborted redemption outcomes.
- Payout execution records now capture intent id, asset, amount, destination, evidence URI, transaction hash, timestamps, and failure reason where applicable.
- Added `GET /executions` to the payout admin surface so operators can inspect live redemption history directly.

### 34. The operator plane now spans mint, merchant settlement, treasury, and payout
- `ops-reporting` no longer stops at mint and treasury data.
- It now also reads:
  - merchant trade lifecycle data from `escrow-gateway`
  - payout execution records from `payoutd`
- Operators can now query:
  - `GET /merchant/trades`
  - `GET /merchant/export`
  - `GET /payout/executions`
  - `GET /payout/export`
- The shared `/summary` response now gives a broader cross-rail operational view across inbound, commerce, treasury, and outbound activity.

### 35. `payoutd` now enforces bank-grade payout screening and approval controls
- Payout policy evaluation no longer stops at per-asset daily cap and soft inventory.
- The payout rail now supports:
  - per-account daily and hourly amount caps
  - per-destination daily amount caps
  - account-level payout velocity limits
  - static destination, account, partner, and region screening
  - approval requirements above configured thresholds
  - approval requirements for high-risk partners and regions
- These controls are evaluated before the treasury wallet broadcast leg and persist failure reasons into the payout execution trail.

### 36. Persistent compliance holds are now available for the payout rail
- Added a persistent hold store in `payoutd` covering:
  - account holds
  - destination holds
  - partner holds
  - region holds
- Operators can now:
  - create a hold
  - list active or historical holds
  - release a hold with actor and notes preserved
- This closes a major risk-control gap by making compliance and fraud holds explicit, reviewable, and durable.

### 37. The production relaunch stack is now scaffolded in-repo
- Added a clean `cmd/payoutd` entrypoint for binary builds.
- Added production scaffolding for:
  - systemd service files
  - env/config templates
  - a non-destructive bring-up script
  - reverse proxy layout documentation
  - a relaunch preflight checklist
- This replaces reliance on the older ad hoc launch scripts as the founder-grade path forward.

### 38. Founder loyalty semantics are now aligned to the spender-first model
- The protocol-wide base loyalty reward no longer follows the payment recipient path.
- Base `ZNHB` rewards are now proposed and settled to the **spender** on qualifying NHB commerce flows.
- This aligns the chain with the founder promise:
  - spend NHB
  - earn ZNHB
- Loyalty integration tests were updated to assert spender-directed reward settlement.

### 39. Founder mainnet now treats `ZNHB` as a fixed-supply asset
- Genesis now preconfigures the protocol loyalty treasury explicitly and starts `ZNHB` with `initialMintPaused = true`.
- Founder genesis files no longer grant `MINTER_ZNHB` as a live operational role.
- This tightens the business model around:
  - fixed-cap `ZNHB`
  - treasury-funded rewards
  - no routine post-genesis `ZNHB` inflation on founder mainnet

### 40. Loyalty defaults were reduced from placeholder inflationary levels to founder-grade economics
- The protocol-wide default base reward is no longer `5000 bps (50%)`.
- Loyalty defaults now center on:
  - `50 bps` target baseline (`0.50%`)
  - `25-100 bps` dynamic operating band (`0.25%-1.00%`)
- Founder genesis now enables the base loyalty treasury with the same 50 bps default.
- This makes the default loyalty posture commercially realistic and materially easier to sustain.

### 41. Treasury-backed reward accounting now replaces inflationary reward semantics
- Staking reward claims are now paid from the configured POTSO/staking treasury instead of implicitly inflating `ZNHB` into circulation.
- Legacy staking snapshots are still honored through compatibility logic so prior reward accounting can settle cleanly from treasury.
- Paymaster auto-top-up documentation and execution semantics were also tightened so top-ups are framed as funded wallet transfers rather than protocol minting.
- The repo documentation now consistently describes:
  - protocol loyalty treasury -> spender
  - business paymaster -> spender
  - treasury-funded staking rewards
  - founder-mainnet fixed-supply `ZNHB`

## Validation Result

Verified green:

```text
go test ./...
```

Additional targeted sweeps also passed during remediation for:
- `./core`
- `./rpc`
- `./services/lendingd/...`
- `./services/ops-reporting`
- `./services/payoutd/...`
- `./services/payments-gateway`
- `./native/swap`
- `./native/escrow`
- `./tests/paymaster`
- `./tests/system`
- `./config`
- `./p2p/integration`
- `./services/swapd/server`
- `./state/bank`
- `./services/swapd/stable`

## Hard-Fork / Rollout Impact

These changes should be treated as a coordinated live-network upgrade:

- transaction hash-domain repair for `IntentRef`
- NHB transfer gas-accounting correction
- removal of the hidden transfer-routing fee behavior

Those affect execution or signing semantics and should not be rolled out as a silent node-only patch on an already-live heterogeneous network.

## What Is Still Not Fully Implemented

The repo is now materially tighter and test-clean, but some founder-vision items remain product/program work rather than bug fixes:

- full treasury control plane for USDT/USDC hot-wallet / cold-wallet mint and redeem operations
- wallet recovery redesign in the separate `nhbportal` wallet repo
- production-grade merchant settlement/reporting surfaces
- formal hard-fork activation plan and migration window for live nodes
- deeper operational runbooks, observability dashboards, and incident controls for financial-network production use

Additional conscious follow-up items still visible in code:

- P2P still requires manual port-forwarding; UPnP / NAT-PMP remains intentionally unimplemented.
- `network/client.go` still carries a retry/backoff TODO for repeated downstream handler failures.
- Generated gRPC `Unimplemented*` stubs remain in protobuf output as expected generated scaffolding, not runtime debt in the node itself.

## Current Conclusion

NHBChain is now in a much stronger state as a transaction and commerce engine:

- the critical signing, transfer, sponsorship, escrow, swap-abort, and startup safety gaps from the audit are closed
- the repo builds and tests cleanly end to end
- the next phase is no longer emergency hardening; it is structured product rollout, fee-policy implementation, treasury operations, and live-network upgrade coordination
- the treasury payout leg itself is now executable; the remaining treasury work is around cold-wallet sweeps, approvals, reserve operations, and richer reconciliation dashboards rather than a missing hot-wallet sender
- treasury control now has live hot-wallet reconciliation and action planning, but it still does not yet execute cold-wallet sweeps or multi-operator approval workflows directly
- treasury control now has persistent maker-checker approval records, but cold-wallet execution still remains external to payoutd and must be carried out by custody / MPC systems
- mint, merchant, treasury, and payout now have a shared operator surface, but finance-grade end-of-day reconciliation, settlement exports, and wallet-integration rollout still remain as the next founder-aligned tranche
