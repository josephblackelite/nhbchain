# Service Directory

Each NHBChain workload is deployed as an independently scalable service. Use the
following references to configure, monitor, and integrate with each component.

## Gateway Service

- **Endpoint:** HTTPS / REST, proxying to lendingd, swapd, governd, and
  consensusd.
- **Responsibilities:** Request authentication, REST to gRPC translation, rate
  limiting, and transaction memo enrichment.
- **Key configuration:** `cmd/gateway` (`cmd/gateway/main.go`) is configured via
  a `-config` flag pointing at a TOML file, plus environment variables
  `NHB_ENV`, `NHB_COMPAT_MODE`, `NHB_GATEWAY_AUTO_HTTPS`, and the per-backend
  upstream URLs `NHB_GATEWAY_LENDING_URL`, `NHB_GATEWAY_SWAP_URL`,
  `NHB_GATEWAY_GOV_URL`, `NHB_GATEWAY_CONSENSUS_URL`. Rate limits are
  configured as structured `cfg.RateLimits` entries in the config file, not a
  single env var.
- **Operational notes:** Deploy at least two replicas behind your public load
  balancer. Gateways should be stateless and read the validator set from the
  consensus service on startup.

## Consensus Service

- **Endpoint:** gRPC on `9090` by default.
- **Responsibilities:** Validates signed envelopes, executes transactions,
  materialises blocks, and exposes deterministic state queries.
- **Key configuration:** `cmd/consensusd` (`cmd/consensusd/main.go`) is
  configured via flags: `-config` (path to `config.toml`, default
  `./config.toml`), `-genesis` (genesis JSON, overrides `NHB_GENESIS`), `-grpc`
  (gRPC listen address, default `127.0.0.1:9090`), and `-p2p` (p2p daemon
  network service address, default `localhost:9091`).
- **Operational notes:** Validators run the consensus service co-located with a
  `p2pd` instance. Horizontally scale read-only replicas for query workloads.

## Lending Service

- **Endpoint:** HTTPS/mTLS REST on `0.0.0.0:9444` (configurable).
- **Responsibilities:** Enforces lending business logic, risk limits, and emits
  health factor telemetry per account.
- **Key configuration:** `services/lending` (`services/lending/config.go`) is
  configured via environment variables: `LEND_NODE_RPC_URL`,
  `LEND_NODE_RPC_TOKEN`, `LEND_SHARED_SECRET_HEADER`, `LEND_SHARED_SECRET`,
  `LEND_TLS_CERT_FILE`, `LEND_TLS_KEY_FILE`, `LEND_TLS_CLIENT_CA_FILE`,
  `LEND_ALLOW_INSECURE`, `LEND_LISTEN` (default `0.0.0.0:9444`),
  `LEND_RATE_PER_MIN`, `LEND_MTLS_REQUIRED`, and `LEND_ALLOWED_CNS`.
- **Operational notes:** Co-locate near the consensus service to minimise
  envelope latency. Configure circuit breakers for price oracle unavailability.

## Oracle Attester Service (`oracle-attesterd`)

- **Endpoint:** HTTP webhook listener on `:8085` by default (NOWPayments IPN
  callback), plus a consensus RPC client connection.
- **Responsibilities:** Verifies inbound payment provider webhooks, mints the
  corresponding swap/collector entries, and signs the resulting attestations
  before submitting them to the consensus service.
- **Key configuration:** `services/oracle-attesterd` (`np_webhook.go`) loads a
  YAML config file with keys including `listen` (default `:8085`),
  `consensus`, `chain_id`, `signer_key`/`signer_key_file`/`signer_key_env`,
  `authority`, `treasury_account`, `collector`, `confirmations`,
  `nowpayments_secret`, `database`, `provider`, `fee`, `assets`, and `evm`.
- **Operational notes:** Run at least three replicas across availability zones.
  Publishers should use mTLS identities issued by the operator.

## P2P Daemon (`p2pd`)

- **Endpoint:** libp2p gossip ports (default `26656`).
- **Responsibilities:** Maintains the gossip mesh, exchanges seed lists, and
  propagates consensus metadata to validators and observers.
- **Operational notes:** Operators deploy `p2pd` alongside every consensus node
  and at edge locations serving RPC read replicas.

Refer to the [migration guide](../migrate/services.md) when upgrading from the
legacy JSON-RPC topology.

---

Additional module references:

- [Escrow Service API](./escrow.md)
