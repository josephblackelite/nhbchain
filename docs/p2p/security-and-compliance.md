# Security & Compliance

## Sybil mitigation

* **Wallet binding:** Every handshake requires a valid NHB/ZNHB wallet signature
  over the canonical payload (`nhb-p2p|hello|payload|ts`). Attackers must
  therefore control a funded wallet to create new identities, dramatically
  increasing the cost of Sybil attacks.
* **Chain/genesis pinning:** Peers are rejected if they advertise a different
  `chainId` or `genesisHash`, preventing forked networks from joining.
* **Timestamp + nonce guard:** Handshakes outside a ±300 second window or with a
  reused nonce are discarded, eliminating replay attempts and stale captures.
* **Reputation system:** Malicious peers rapidly accumulate negative scores and
  are first greylisted and ultimately banned. Persistent peers may be greylisted
  but are never banned, ensuring validator liveness.
* **Rate limits:** Coordinated floods from a single host are throttled via the
  per-IP token bucket. Per-peer and global buckets ensure resilience even when
  the attack originates from many hosts.

## Compliance and audit readiness

Regulators and institutional partners often require demonstrable controls:

* **Immutable audit trail:** P2P events (handshakes, disconnects, rate-limit
  hits, bans) are logged. Retain these logs for forensic review.
* **Configuration baselines:** Store version-controlled copies of `config.toml`
  highlighting `[p2p]` settings. Include change approvals where applicable.
* **RPC evidence:** Capture periodic snapshots of `p2p_info` and `p2p_peers` to
  demonstrate the active peer set, configuration, reputation scores, and
  enforcement actions (`greylisted`, `banned`, `firstSeen`, `lastSeen`).
* **Reproducible peer view:** Persistent peer records allow auditors to compare
  observed connections against expected bootnodes/persistent peers and verify
  quorum membership.
* **Key custody:** Wallet signatures tie peers to real accounts. Maintain secure
  custody procedures (hardware wallets, HSMs, or KMS) and document signing key
  rotations.
* **Penetration tests:** Periodically validate that greylist/ban thresholds and
  rate limits respond as expected by simulating load and malformed traffic in a
  controlled environment.

These measures showcase robust access controls, monitoring, and incident
response capabilities suitable for due diligence processes.
