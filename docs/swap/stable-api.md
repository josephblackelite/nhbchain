# Stable Swapd API (Preview)

> **Note**: The stable engine is under active development. The current implementation exposes
> endpoints that respond with `501 Not Implemented` until the pricing, reservation, and
> cash-out flows are fully wired in.

## Configuration

The `services/swapd/config.yaml` file now contains an optional `stable` section to describe
preview parameters for the future engine:

```yaml
stable:
  paused: true
  quote_ttl: "1m"
  max_slippage_bps: 50
  soft_inventory: 1000000
  assets:
    - symbol: ZNHB
      base: ZNHB
      quote: USD
```

Setting `paused: true` keeps the engine disabled while allowing operators to stage
asset definitions. When `paused` is `false`, at least one asset entry is required.

## HTTP Endpoints

The server reserves the following paths:

- `POST /v1/stable/quote`
- `POST /v1/stable/reserve`
- `POST /v1/stable/cashout`
- `GET /v1/stable/status`
- `GET /v1/stable/limits`

All endpoints currently return:

```json
{"error": "stable engine not enabled"}
```

Future revisions will flesh out request/response schemas and enforcement logic.
