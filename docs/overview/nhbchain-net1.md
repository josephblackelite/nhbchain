# NHBCHAIN NET-1 Delivery Notes

These notes document the NHBCHAIN NET-1 hardening work so protocol engineers,
operations, and partner teams have a canonical reference for the shipped
changes.

## Scope delivered

- **Authenticated peer admission** using a signed handshake carrying chain ID,
genesis hash, node ID, and client version. Mismatches immediately disconnect
the session and surface clear log lines.
- **Network hygiene controls** including per-peer and global token-bucket rate
limits, lightweight peer scoring with automatic bans, and exponential
backoff when redialing persistent peers.
- **Operator configuration** surfacing peer-count limits, IO deadlines, rate
limits, and bootnode lists through `config.toml` and the companion sample
files wired into `cmd/nhb` startup.
- **Quality gates** backed by unit tests for handshake mismatches, rate limit
enforcement, bootnode dialing, and configuration parsing to keep regressions
from landing.
- **Swap documentation** extended with deep dives for frontend developers,
JSON-RPC integrations, auditors, regulators, investors, and end consumers.

## P2P handshake and identity

See [docs/p2p.md](./p2p.md) for the full wire format. Each handshake is signed
with the node's secp256k1 key and includes a random nonce to prevent replay.
The server verifies the signature, recomputes the node ID from the presented
public key, and enforces `chainId`/`genesisHash` equality before the peer is
admitted. Any violation disconnects the socket prior to message exchange.

## Message safety & abuse management

Per-peer token buckets refill at `MaxMsgsPerSecond` with a burst of double that
rate, while a global bucket ensures aggregate traffic stays within safe bounds.
Peers earn or lose reputation based on message quality, with bans applied when
the score falls to `-6` or when a one-minute invalid traffic window exceeds 50%
invalid frames. Persistent peers automatically re-dial after disconnects, using
backoff up to one minute.

## Configuration surface

`config.toml`, `config-local.toml`, and `config-peer.toml` expose the following
tunables:

- `MaxPeers`, `MaxInbound`, `MaxOutbound`
- `Bootnodes`, `PersistentPeers`
- `PeerBanSeconds`
- `ReadTimeout`, `WriteTimeout`
- `MaxMsgBytes`, `MaxMsgsPerSecond`
- `ClientVersion`

`cmd/nhb/main.go` plumbs these values into the P2P server at startup. Nodes log
the configured chain ID, genesis hash summary, and client version during boot,
making misconfiguration easy to spot.

## Test coverage

`go test ./p2p/... -v` and `go test ./config -v` validate the handshake,
rate-limit enforcement, bootnode dialing, and configuration parsing paths
introduced in this work.

## Swap documentation expansion

The swap gateway guide at [docs/swap/README.md](../swap/README.md) now includes:

- **JSON-RPC integration** details for `swap_submitVoucher`, request/response
examples, and operational error handling guidance.
- **Frontend implementation** guidance covering user flows, UI state machine,
validation, and security controls required for quoting, order creation,
and settlement visibility.
- **Audience-specific sections** tailored for auditors, regulators, investors,
and consumers with expectations around record keeping, compliance, metrics, and
user education.

These additions ensure downstream teams have the context needed to build and
review products on top of the hardened network stack and swap minting flow.
