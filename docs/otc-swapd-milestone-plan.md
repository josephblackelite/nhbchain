# OTC / `services/swapd` — discovery pass and first-milestone plan

Status as of 2026-08-02. This is a scoping document (task `#54` from `docs/issue30.md`'s
"Path to Phase D" tracker) — no code changed as part of this pass. Full technical
detail behind every claim below was gathered by direct source inspection; file:line
references are given so each claim can be re-verified.

**Decision confirmed 2026-08-02**: `services/otc-gateway` owns partner
onboarding/KYB/approval; `services/swapd` is the pure execution/quoting
backend it calls. Swapd does not grow its own partner/KYB system. This
decision governs every later item in this doc's milestone plan (step 1).

## Headline finding

**The user-facing "OTC partner onboarding" system (KYB dossiers, approve/reject
workflow, persisted partner records) is not part of `services/swapd` at all.** It
lives in a separate, parallel service, `services/otc-gateway`, with its own
Postgres schema and its own docs (`docs/otc/*.md`). `services/swapd` is a thinner
quoting/reservation engine whose "partner auth" is a static YAML list of
API-key/HMAC-secret pairs — no KYB, no approval workflow, no persisted partner
record at all. **These two services do not talk to each other today.** Any real
institutional-partner build has to either connect them or explicitly decide one
isn't needed — this is the first product decision below, and it changes the
shape of everything after it.

Independently reconfirmed from a prior investigation (`docs/issue30.md:63-69`):
swapd is real, load-bearing, CI-green infrastructure the chain node depends on to
start — not dead code — but no commit has touched it in ~3.5 months, no live
deployment exists anywhere, and `nhbportal`'s `SWAPD_API_URL` is unset in both
`.env` and `.env.remote`.

## What exists vs. what's missing

| Area | Exists | Missing |
|---|---|---|
| Quoting engine | Real, tested (`services/swapd/stable/engine.go`, `engine_test.go`) | Never receives a live price in the standalone process — `main.go` never wires `oracle.WithPublisher` to call `engine.RecordPrice`, so `/v1/stable/quote` would 503 in production |
| Reservation/cash-out | Real state machine, SQLite-persisted (`stable/reserve.go`, `stable/cashout.go`) | Pure bookkeeping — no chain call, no bank/stablecoin call anywhere in the path |
| Partner credentials | HMAC-SHA256 scheme is real and reasonable (`server/partner_auth.go`) | No partner data model, no onboarding API, no hot-reload (adding a partner means hand-editing YAML + restarting the process) |
| KYB/compliance | Exists in a *different* service (`otc-gateway`), and is itself simulated there (`docs/dev/otc-auth-baseline.md:10` — WebAuthn is a header check, no cryptographic verification) | No connection between otc-gateway approval and swapd credential issuance |
| Feature flag | Real, correctly gates the engine (`stable.paused`, `config/config.go:126`) | Never flipped to `false` in any checked-in environment (dev/staging/prod Helm overlays all omit the `stable:` block); no partner has ever been configured to test it |
| CI | Real, green, runs a full Postman regression every push (`.github/workflows/ci.yml:114-138`) | — |
| Deploy pipeline | Builds/pushes Docker + Helm artifacts on every push to main (`deploy.yml`) | Never applied to any cluster — no `kubectl apply`/`helm upgrade` step exists; no systemd unit (contrast: `otc-gateway.service` and `payments-gateway.service` both exist) |
| Settlement | A human manually attaches an "ACH/SWIFT hint" to a timestamp, out of band (`docs/swap/stable-api.md:125`) | No receipt/settlement endpoint in `stable_handlers.go`; no reconciliation against real bank/stablecoin balances |
| Audit trail | OTel spans + `slog` lines | No durable business-audit table in swapd (contrast: otc-gateway has one, `docs/otc/audit.md`) |
| Trade sizing | Config knobs exist | Checked-in values are smoke-test scale (`mint_limit: 10` in the default config), not sized for real OTC notional |

## Why "wire it up" is bigger than it sounds

Today there are, in effect, **three disconnected partner-facing surfaces**:
1. The main node's own swap JSON-RPC (`rpc/http.go:1233`, `swap_submitVoucher`) — used today by `otc-gateway`'s real voucher-signing flow (`docs/otc/voucher.md`).
2. `swapd`'s own `/v1/stable/*` REST API — used by nobody in production.
3. `otc-gateway`'s `/api/v1/*` KYB/invoice workflow.

A compat mapping exists (`gateway/compat/mapping.go:19-26`) that maps swap RPC
method names to swapd REST paths for a future gateway-proxy migration, but swapd
never actually registers those routes — that mapping would 404 if exercised.

The project's own most recent internal docs already reached the same
conclusion independently: `docs/nhbgtm2026.md:138` — *"Swap-out (withdrawal)
does not work end to end. Four disconnected pieces, none forming a complete
path... swapd's engine, even if deployed, never touches a real user's NHB
balance or burns anything — pure internal counters."* And `docs/nhbgtm2026.md:73-80`
(Phase B item 9, the custody-model decision) settled the **retail** buy/redeem
flow onto NOWPayments-as-custodial-rail — that decision is scoped to retail,
not institutional OTC block trading, and does not wire swapd to any real
settlement rail.

## Compliance/risk gaps (flagged, not designed)

- No KYB/AML in swapd itself; the one that exists (in otc-gateway) has a
  trivially-forgeable "OIDC" token (`subject|role` split on a pipe) and
  simulated WebAuthn.
- No linkage between otc-gateway approval and swapd credential issuance.
- No trade-size tiering for institutional block volume — current caps are
  smoke-test scale.
- No durable audit trail in swapd.
- No dispute/reversal handling once a cash-out intent exists.
- No settlement reconciliation against real balances — entirely manual today.
- Rate limiting is partner-declared only, never validated against settled volume.

## Proposed first milestone: onboard one real institutional partner, execute one real trade, safely

Assumes the decision is "keep building toward a real OTC-desk launch on this
engine" (per the plan's "Decided today" section — OTC is permanent
infrastructure) rather than deprecating swapd. Scoped to the minimum real, safe
path — not a redesign of everything above.

1. **Product decision: otc-gateway vs. swapd overlap.** Before any code:
   decide whether the institutional partner's KYB/approval record lives in
   otc-gateway's Postgres (real schema already exists) with swapd as a pure
   execution/quoting backend it calls, or whether swapd grows its own minimal
   partner table. **Recommendation: the former** — don't duplicate a KYB
   workflow that already exists elsewhere in the stack.
2. **Fix the oracle→engine wiring gap** in the standalone swapd binary
   (`services/swapd/main.go`) so `/v1/stable/quote` can return a real price
   outside of tests. Small and mechanical, but blocks everything downstream.
3. **Stand up swapd for real, in staging first.** Add the missing systemd
   unit or confirm the Helm chart is actually applied to a real host with a
   real hostname, real TLS, and a real admin bearer token/mTLS CA — none of
   this exists as live infrastructure today, only as CI/local-only config.
4. **Provision exactly one real partner**, sized for the actual notional this
   partner will trade — the current toy config values (`mint_limit: 10`) are
   not usable as-is.
5. **Decide and build the actual settlement leg** — the single biggest real
   gap. Pick one: (a) extend the already-decided NOWPayments-custody model to
   cover institutional size (simplest, reuses a direction already chosen), or
   (b) build a manual-but-audited treasury-wallet/bank-wire confirmation step
   with a real "attach receipt" endpoint and matching DB table. Do not leave
   this as an unstructured free-text field.
6. **Add a minimal durable audit trail to swapd** (an `events`-style table,
   mirroring otc-gateway's pattern) so every quote/reserve/cashout/settlement
   step for this first partner is reconstructable outside of log-grepping.
7. **Run one real trade end-to-end in staging** with real but small notional:
   quote → reserve → cashout → manual settlement confirmation →
   reconciliation against swapd's own ledger counters. Capture as the
   Newman/`make audit:endpoints` regression baseline plus a manual sign-off.
8. **Only after step 7 succeeds once**, flip `stable.paused=false` in
   production for this one partner, keeping the on-chain `global.pauses.swap`
   toggle as a kill switch (the two-switch model is already documented,
   `docs/swap/service.md:98-109`).
9. **Fix the still-open, unrelated critical bug** (`docs/issue30.md:55`,
   `applySwapPayoutReceipt` authorization — trusts a payload field instead of
   the recovered signer) before any real money flows through the adjacent
   `native/swap` machinery, in case step 5 ends up reusing any part of it.

Items 2, 6, and 9 are small and mechanical. Items 1, 3, 4, and 5 are the real
scope and need explicit product/founder decisions before engineering starts.
