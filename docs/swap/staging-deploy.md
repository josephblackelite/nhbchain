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

## Configuring the price-proof signer

`price_proof.signer.type` selects which of two signer implementations signs
`swap.PriceProof` payloads at `POST /v1/price-proof`. See
`deploy/config/swapd.staging.yaml.example` for the two commented-out
`signer:` blocks this section describes.

- **`hsm`** (default when `type` is omitted) -- an mTLS-fronted HSM proxy.
  Requires real HSM infrastructure (`base_url`, `key_label`, `ca_cert`,
  `client_cert`, `client_key`).
- **`local`** -- a local encrypted keystore file, using this repo's own
  standard Ethereum V3 keystore encryption (the same mechanism already used
  for validator keys -- see `crypto/keystore.go`). Use this if you want to
  reuse a wallet you already hold rather than provisioning HSM
  infrastructure.

  1. Create the keystore file with `nhb-cli keystore import`. This reads the
     raw private key and the encryption passphrase from environment
     variables ONLY -- never a CLI argument (which would leak into shell
     history and process listings) and never a config file:

     ```bash
     NHB_KEYSTORE_IMPORT_PRIVATE_KEY=<hex-key> \
     NHB_KEYSTORE_IMPORT_PASSPHRASE=<passphrase> \
       nhb-cli keystore import --out /etc/nhbchain/secrets/swapd-price-signer.keystore.json
     ```

     The command writes the encrypted file, then immediately re-reads and
     decrypts it with the same passphrase to confirm the write succeeded,
     and prints the resulting public address. **Compare that printed
     address against the wallet you intended to import before trusting the
     file for anything** -- the command has no independent way to know
     whether the correct key was supplied.

  2. Set `price_proof.signer.type: local`, `keystore_path` to that file's
     path, and `passphrase_env` to the NAME of an environment variable (not
     the passphrase itself) that swapd will read at startup, e.g.
     `SWAPD_PRICE_SIGNER_PASSPHRASE`.

  3. Set that environment variable for the swapd process -- e.g. via
     `deploy/systemd/swapd.service`'s `EnvironmentFile=`, pointing at a
     root-only-readable file, never inline in the unit file or in
     `swapd.yaml` itself.

  swapd decrypts the keystore exactly once, at startup. If the passphrase is
  wrong or the file is missing/corrupted, swapd refuses to start (fails
  loudly) rather than falling back to running with no working signer.

Whichever type you choose, the referenced key MUST be distinct from any
mint-voucher-signing key used elsewhere (e.g. otc-gateway's `MINTER_ZNHB`
key) -- a price-signer key must not carry the ZNHB mint-authority key's
blast radius. And regardless of signer type, nothing is actually verified
on-chain until the corresponding address is registered via a governance
`policy.swapPriceSigner` proposal (see `docs/gov/proposal-types.md`) under
the exact `provider` string this config uses.

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
