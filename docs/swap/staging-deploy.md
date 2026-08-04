# swapd Staging Deploy Runbook

Operator steps to get `swapd` running on a staging EC2/VM host via systemd.
For local dev, use `deploy/compose/docker-compose.yml` instead -- this
runbook is for a traditional host deploy.

## 1. Build

```bash
GOOS=linux GOARCH=amd64 go build -trimpath -o swapd ./services/swapd
```

## 2. Install the binary

Copy `swapd` to the target host at `/opt/nhbchain/bin/swapd`, owned
`nhb:nhb`, mode `755`:

```bash
scp swapd staging-host:/tmp/swapd
ssh staging-host 'sudo install -o nhb -g nhb -m 755 /tmp/swapd /opt/nhbchain/bin/swapd'
```

## 3. Install config

Copy `deploy/config/swapd.staging.yaml.example` to the host at
`/etc/nhbchain/swapd.yaml`, then edit it in place to fill in real values:

- TLS certificate/key under `admin.tls` (and `admin.mtls.client_ca` if mTLS
  stays enabled)
- `admin.bearer_token_file` -- generate a real token and write it to that path
- At least one real partner. Use the `swapd-partner-admin` CLI (a separate
  tool under `cmd/swapd-partner-admin`) to add partner credentials safely
  (atomic write, generated with `crypto/rand`, printed once) rather than
  hand-editing the `stable.partners` list:

  ```bash
  swapd-partner-admin add --config /etc/nhbchain/swapd.yaml --id <partner-id>
  ```

## 4. Install the systemd unit

```bash
scp deploy/systemd/swapd.service staging-host:/tmp/swapd.service
ssh staging-host 'sudo install -m 644 /tmp/swapd.service /etc/systemd/system/swapd.service && \
  sudo systemctl daemon-reload && \
  sudo systemctl enable --now swapd'
```

## 5. Verify

```bash
ssh staging-host systemctl status swapd
ssh staging-host curl -sk https://127.0.0.1:7074/healthz
```

## Choosing a settlement rail

`stable.settlement.default_rail` controls how cash-out intents settle by
default; any partner can override it via `stable.partners[].settlement_rail`.

- **`nowpayments`** -- automated mass payout via the real NOWPayments
  account. This is the active rail today (product decision), and what this
  template ships with. Requires real NOWPayments account credentials
  (`stable.settlement.nowpayments.email_file` / `password_file` /
  `api_key_file`) -- note this is a *different* NOWPayments credential type
  than the simple `api_key` used by the oracle price-feed source under
  `sources:` above; the payout API requires full account login (email +
  password) for a JWT, not just an API key.
- **`manual_treasury`** -- no external credentials needed. An operator wires
  funds by hand and confirms the settlement via the admin API. This is the
  rail to move to once regulatory requirements for full manual treasury are
  met -- it's also the automatic fallback if `default_rail` is ever left
  unset, so a misconfigured deployment never silently attempts an automated
  payout.

## What this runbook does NOT get you

Completing the steps above gets you a reachable staging `swapd` instance --
nothing more. It does not mean:

- **A real partner is onboarded.** That's a separate business step: a real
  institutional partner relationship must exist first, then real credentials
  are issued via `swapd-partner-admin`.
- **Real settlement funds are flowing.** `nowpayments` requires a funded,
  live NOWPayments account; `manual_treasury` requires an operator to
  actually wire funds and confirm each settlement by hand via the admin API.
  Neither happens automatically just because the service is running.
