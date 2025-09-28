# Swap Mint Risk Controls

The SWAP-4 upgrade introduces deterministic guardrails around on-ramp mints so operations teams can prove adherence to policy while regulators and investors can audit behaviour. The limits below are enforced inside `swap_submitVoucher` before any state changes and violations raise structured alerts.

## Configuration

Configure limits in `config.toml` under `[swap.risk]` and the provider allow list under `[swap.providers]`.

```toml
[swap.risk]
PerAddressDailyCapWei = "10000e18"
PerAddressMonthlyCapWei = "300000e18"
PerTxMinWei = "1e18"
PerTxMaxWei = "50000e18"
VelocityWindowSeconds = 600
VelocityMaxMints = 5
SanctionsCheckEnabled = true

[swap.providers]
Allow = ["nowpayments"]
```

| Setting | Description |
| --- | --- |
| `PerTxMinWei` | Reject vouchers below this mint amount. `0` disables the check. |
| `PerTxMaxWei` | Reject vouchers above this mint amount. |
| `PerAddressDailyCapWei` | Aggregate recipient mints per UTC day and block the next mint when the cap is exceeded. |
| `PerAddressMonthlyCapWei` | Aggregate per calendar month using UTC. |
| `VelocityWindowSeconds` & `VelocityMaxMints` | Count successful mints inside a rolling window and block the next mint when the count reaches `VelocityMaxMints`. |
| `SanctionsCheckEnabled` | When `true`, run the sanctions hook before minting. |
| `[swap.providers].Allow` | Lower-case allow list of PSP identifiers. Empty array allows all providers. |

All numeric values accept scientific notation (e.g. `10000e18`).

## Runtime Behaviour

1. **Provider allow-list** – Rejects vouchers whose `provider` field is not in the allow list. Emits `swap.alert.limit_hit` with `limit=provider`.
2. **Sanctions hook** – Calls the configured checker. A `false` response blocks the mint and emits `swap.alert.sanction`.
3. **Per-transaction limits** – Enforced before checking historical buckets. Violations emit `swap.alert.limit_hit` with `limit=per_tx_min` or `per_tx_max`.
4. **Daily & monthly caps** – UTC buckets keyed per address. Hitting a cap blocks the mint and emits `swap.alert.limit_hit` with `limit=daily_cap` or `monthly_cap`.
5. **Velocity window** – Evaluates mints inside the configured rolling window and emits `swap.alert.velocity` when the threshold is met. The event reports `windowSeconds`, `allowedMints`, and the observed count.

All alerts are appended to the state event log for audit trails.

## Example: Policy Update

Use governance or manual updates to raise limits temporarily:

```toml
[swap.risk]
PerAddressDailyCapWei = "25000e18"
PerAddressMonthlyCapWei = "500000e18"
PerTxMinWei = "5e18"
PerTxMaxWei = "75000e18"
VelocityWindowSeconds = 900
VelocityMaxMints = 3
SanctionsCheckEnabled = true
```

Restart the node (or trigger a config reload) after editing the file to apply the new guardrails.

## Monitoring

* Subscribe to the event stream for:
  * `swap.alert.limit_hit`
  * `swap.alert.velocity`
  * `swap.alert.sanction`
* Inspect counters via the new RPC:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "swap_limits",
  "params": ["nhb1recipientaddress000000000000000000000000"]
}
```

The response includes day/month totals, remaining capacity, and velocity observations for the address.

## Operational Tips

* Use `go run ./examples/docs/ops/read_pauses` to double-check that the swap
  module remains active before investigating PSP escalations. Engage governance
  with `go run ./examples/docs/ops/pause_toggle --module swap --state pause`
  when you need to halt minting.
* Set `VelocityWindowSeconds` high enough to account for expected PSP bursts but low enough to prevent scripted abuse.
* Use the optional `cmd/swap-audit` tool to print the currently loaded configuration: `go run ./cmd/swap-audit --config ./config.toml`.
* When raising limits for incident response, document the change in the compliance log and ensure alerts are reviewed for potential abuse attempts.
