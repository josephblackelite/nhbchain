# English Security Audit

## Summary

- ❌ Transport & Auth
- ❌ Secrets & Logs
- ❌ Replay & Nonce
- ⚠️ Funds Safety
- ⚠️ Fee / Free-tier Rollover
- ⚠️ Pauses & Governance
- ⚠️ DoS & QoS
- ✅ File Serving & Path Traversal
- ✅ External Dependencies

## Transport & Auth

Assess TLS coverage, RPC authentication, and leakage of credentials in logs.

### Insecure transport enabled (ERROR)

AllowInsecure=true exposes RPC traffic over plaintext

**Proofs:**

- `cmd/consensusd/main.go:571`

````
} else {
		return false, nil, fmt.Errorf("network security configuration is missing TLS material; set AllowInsecure=true only for development")
	}
````

**Remediation:**

- Require TLS by disabling AllowInsecure or guarding with loopback-only binds.

### Production config allows insecure transport (ERROR)

Configuration permits plaintext transport

**Proofs:**

- `config-local.toml:1`

````
ListenAddress = "0.0.0.0:6002"
````

**Remediation:**

- Set allowinsecure=false in production configs and document TLS setup.

### Development sample locks down transport (INFO)

`config/bad-sample.toml` now binds to localhost and disables plaintext fallbacks by default so that the example cannot be misused in production.

**Proofs:**

- `config/bad-sample.toml:1`

````
# --------------------------------------------------------------
# WARNING: Development-only sample configuration.
````

**Remediation:**

- None. Keep the prominent warning and loopback bindings so operators understand the risks of enabling insecure transport.

### Insecure transport enabled (ERROR)

AllowInsecure=true exposes RPC traffic over plaintext

**Proofs:**

- `network/security.go:83`

````
default:
		return nil, nil, nil, fmt.Errorf("network security configuration is missing TLS material; set AllowInsecure=true only for development")
	}
````

**Remediation:**

- Require TLS by disabling AllowInsecure or guarding with loopback-only binds.

### Authorization header logged (ERROR)

Authorization values must never be printed in logs.

**Proofs:**

- `sdk/pos/examples/create_intent.go:113`

````
fmt.Printf("Authorization reference: %s\n", resp.GetAuthorizationId())
}
````

**Remediation:**

- Redact Authorization header before logging or remove the log entry.

### Insecure transport enabled (ERROR)

AllowInsecure=true exposes RPC traffic over plaintext

**Proofs:**

- `tools/audit/english/checks.go:70`

````
Title:       "Insecure transport enabled",
						Description: "AllowInsecure=true exposes RPC traffic over plaintext",
						Remediation: "Require TLS by disabling AllowInsecure or guarding with loopback-only binds.",
````

**Remediation:**

- Require TLS by disabling AllowInsecure or guarding with loopback-only binds.

### Static bearer token detected (WARN)

Static Authorization bearer tokens should rotate or rely on JWT with expiry.

**Proofs:**

- `services/otc-gateway/auth/auth.go:97`

````
//
//	Authorization: Bearer <redacted>
//	X-WebAuthn-Verified: true
````

**Remediation:**

- Replace static tokens with short-lived JWTs or signed challenges.

### JWT verification disabled (WARN)

JWT configuration skips verification which weakens authn.

**Proofs:**

- `tools/audit/english/checks.go:1`

````
package english
````

**Remediation:**

- Enable verification and enforce signature/key rotation for JWT tokens.

### JWT verification disabled (WARN)

JWT configuration skips verification which weakens authn.

**Proofs:**

- `tools/audit/english/patterns.go:1`

````
package english
````

**Remediation:**

- Enable verification and enforce signature/key rotation for JWT tokens.

## Secrets & Logs

Ensure secrets are stored outside the repo and redacted from telemetry.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/internal/passphrase/source.go:56`

````
fmt.Fprint(os.Stderr, "Enter validator keystore passphrase: ")
		bytes, err := term.ReadPassword(int(os.Stdin.Fd()))
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/nhb/main.go:294`

````
if !found {
			fmt.Printf("Ignoring seed %q: missing node ID\n", trimmed)
			continue
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/nhb/main.go:300`

````
if node == "" || addr == "" {
			fmt.Printf("Ignoring seed %q: empty components\n", trimmed)
			continue
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/nhb/main.go:314`

````
if rawRegistry, ok, err := node.NetworkSeedsParam(); err != nil {
		fmt.Printf("Failed to load network.seeds: %v\n", err)
	} else if ok {
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/nhb/main.go:318`

````
if parseErr != nil {
			fmt.Printf("Failed to parse network.seeds: %v\n", parseErr)
		} else {
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/nhb/main.go:325`

````
if resolveErr != nil {
				fmt.Printf("DNS seed resolution failed: %v\n", resolveErr)
			}
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/p2pd/main.go:136`

````
if !found {
			fmt.Printf("Ignoring seed %q: missing node ID\n", trimmed)
			continue
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/p2pd/main.go:142`

````
if nodeID == "" || addr == "" {
			fmt.Printf("Ignoring seed %q: empty components\n", trimmed)
			continue
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/p2pd/main.go:162`

````
if rawRegistry, ok, err := manager.ParamStoreGet("network.seeds"); err != nil {
				fmt.Printf("Failed to load network.seeds: %v\n", err)
			} else if ok {
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/p2pd/main.go:166`

````
if parseErr != nil {
					fmt.Printf("Failed to parse network.seeds: %v\n", parseErr)
				} else {
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `cmd/p2pd/main.go:173`

````
if resolveErr != nil {
						fmt.Printf("DNS seed resolution failed: %v\n", resolveErr)
					}
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Private key material committed (ERROR)

Key files are present in the repository.

**Proofs:**

- `ops/audit-pack/seeds-fixtures/validator_priv.key:1`

````
binary/secret material
````

**Remediation:**

- Remove the key from the repo, rotate credentials, and load via secure secret manager.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `ops/seeds/tools/dnsstub/main.go:78`

````
go func() {
		log.Printf("seed DNS stub listening on %s for %s", *listenAddr, fqdn)
		if err := server.ListenAndServe(); err != nil {
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `ops/seeds/tools/dnsstub/main.go:100`

````
_ = tcpServer.ShutdownContext(shutdownCtx)
	log.Println("seed DNS stub shut down")
}
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `p2p/connmanager.go:154`

````
if err := m.server.Connect(seed.Address); err != nil {
			fmt.Printf("Seed dial %s (%s) failed: %v\n", seed.Address, seed.NodeID, err)
			m.markFailure(seed)
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `p2p/connmanager.go:186`

````
if err := m.store.Put(entry); err != nil {
				fmt.Printf("Persist seed %s: %v\n", seed.Address, err)
			}
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `p2p/connmanager.go:213`

````
if _, err := m.store.RecordFail(seed.NodeID, m.now()); err != nil {
		fmt.Printf("Record seed failure %s: %v\n", seed.NodeID, err)
	}
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `p2p/pex.go:309`

````
if !found {
			fmt.Printf("Ignoring seed %q: missing node ID\n", trimmed)
			continue
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `p2p/pex.go:314`

````
if node == "" {
			fmt.Printf("Ignoring seed %q: empty node ID\n", trimmed)
			continue
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `p2p/pex.go:319`

````
if _, _, err := net.SplitHostPort(addr); err != nil {
			fmt.Printf("Ignoring seed %q: invalid address: %v\n", trimmed, err)
			continue
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `p2p/server.go:113`

````
if err != nil {
			fmt.Printf("Ignoring seed %q@%q: %v\n", strings.TrimSpace(origin.NodeID), strings.TrimSpace(origin.Address), err)
			continue
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `p2p/server.go:303`

````
if err := s.refreshSeedRegistry(); err != nil {
				fmt.Printf("Seed registry refresh failed: %v\n", err)
			}
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Sensitive secret logged (ERROR)

Secrets should not appear in log statements.

**Proofs:**

- `p2p/server.go:576`

````
if err := server.refreshSeedRegistry(); err != nil {
			fmt.Printf("Seed registry lookup failed: %v\n", err)
		}
````

**Remediation:**

- Remove secret from logs and use redaction helpers.

### Private key material committed (ERROR)

Key files are present in the repository.

**Proofs:**

- `services/governd/config/server.key:1`

````
binary/secret material
````

**Remediation:**

- Remove the key from the repo, rotate credentials, and load via secure secret manager.

## Replay & Nonce

Transactions must enforce nonce/expiry semantics to prevent replay.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `clients/ts/pos/realtime.ts:1`

````
// Code generated by protoc-gen-ts_proto. DO NOT EDIT.
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `clients/ts/pos/registry.ts:1`

````
// Code generated by protoc-gen-ts_proto. DO NOT EDIT.
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `cmd/nhb-cli/claimable_cmd.go:1`

````
package main
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `cmd/nhb-cli/pos.go:1`

````
package main
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `consensus/proposer.go:1`

````
package consensus
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `core/claimable/claimable.go:1`

````
package claimable
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `core/events/claimable.go:1`

````
package events
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `core/pos_stream.go:1`

````
package core
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `core/state/pos_registry.go:1`

````
package state
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `deploy/compose/Dockerfile:1`

````
# syntax=docker/dockerfile:1.6
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `deploy/compose/config/consensus.toml:1`

````
ListenAddress = "0.0.0.0:6002"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `deploy/compose/config/gateway.yaml:1`

````
listen: ":8080"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `deploy/compose/config/lendingd.yaml:1`

````
listen: ":50053"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `deploy/compose/config/p2p.toml:1`

````
ListenAddress = ":26656"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `deploy/compose/config/swapd.yaml:1`

````
listen: ":7074"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `deploy/compose/docker-compose.audit-e2e.yaml:1`

````
services:
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `deploy/compose/docker-compose.yml:1`

````
version: "3.9"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/api/pos-realtime.md:1`

````
# POS realtime finality stream
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/changelogs/POS-OPS-10.md:1`

````
# POS-OPS-10: Merchant onboarding & device attestation runbooks
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/changelogs/POS-QOS-3.md:1`

````
# POS-QOS-3
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/changelogs/POS-REFUND-6.md:1`

````
# POS-REFUND-6 – Refund linkage & over-refund guard
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/changelogs/POS-REGISTRY-4.md:1`

````
# POS-REGISTRY-4 — Merchant/Device Registry & Pause Controls
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/changelogs/POS-RT-7.md:1`

````
# POS-RT-7: realtime finality stream
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/changelogs/POS-SDK-9.md:1`

````
# POS-SDK-9: NHB-Pay spec + NFC/NDEF + SDK examples
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/examples/gov/fee-policy-proposal.json:1`

````
{
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/examples/gov/fee-routing-proposal.json:1`

````
{
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/examples/identity/claim.http:1`

````
### Claim funds (JSON-RPC)
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/examples/identity/create-claimable.http:1`

````
### Create claimable for email hash (JSON-RPC)
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/runbooks/pos-onboarding.md:1`

````
# POS Merchant & Device Onboarding Runbook
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/security/repository-pgp-key.asc:1`

````
-----BEGIN PGP PUBLIC KEY BLOCK-----
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `docs/specs/pos-qos.md:1`

````
# POS-QOS-3: Priority Mempool and Proposer Quotas
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/grafana-provisioning/dashboards/dashboards.yaml:1`

````
apiVersion: 1
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/grafana-provisioning/datasources/datasources.yaml:1`

````
apiVersion: 1
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/lendingd.yml:1`

````
version: "3.8"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/loki-config.yaml:1`

````
server:
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/mininet/Dockerfile.consensusd:1`

````
# syntax=docker/dockerfile:1.6
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/mininet/Dockerfile.p2pd:1`

````
# syntax=docker/dockerfile:1.6
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/mininet/config/consensus.toml:1`

````
ListenAddress = "0.0.0.0:6002"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/mininet/config/p2p.toml:1`

````
ListenAddress = ":26656"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/mininet/docker-compose.yml:1`

````
version: "3.9"
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/observability.yml:1`

````
version: '3.9'
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/compose/tempo.yaml:1`

````
server:
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/docs/ts/query-positions.ts:1`

````
import path from 'node:path';
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/gov/new_param_proposal.ts:1`

````
import { credentials } from "@grpc/grpc-js";
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/queries/lending_positions.go:1`

````
package main
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/queries/ts/query_proposals.ts:1`

````
import * as grpc from "@grpc/grpc-js";
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/wallet-lite/app/api/identity/claim/route.ts:1`

````
import { NextRequest, NextResponse } from 'next/server';
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `examples/wallet-lite/app/api/payments/claimables/route.ts:1`

````
import { NextRequest, NextResponse } from 'next/server';
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `native/pos/registry.go:1`

````
package pos
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `proto/pos/realtime.pb.go:1`

````
// Code generated by protoc-gen-go. DO NOT EDIT.
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `proto/pos/realtime.proto:1`

````
syntax = "proto3";
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `proto/pos/realtime_grpc.pb.go:1`

````
// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `proto/pos/registry.pb.go:1`

````
// Code generated by protoc-gen-go. DO NOT EDIT.
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `proto/pos/registry.proto:1`

````
syntax = "proto3";
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `proto/pos/registry_grpc.pb.go:1`

````
// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `proto/pos/tx_grpc.pb.go:1`

````
// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `rpc/claimable_handlers.go:1`

````
package rpc
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `scripts/pos_readiness.sh:1`

````
#!/usr/bin/env bash
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `sdk/pos/examples/subscriber.ts:1`

````
// Sample realtime subscriber demonstrating gRPC and WebSocket reconnect logic.
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `services/payments-gateway/kms.go:1`

````
package main
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `services/payments-gateway/node_client.go:1`

````
package main
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `services/swap-gateway/nowpayments.go:1`

````
package main
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

### Missing nonce/ttl validation (ERROR)

Critical transaction paths should enforce nonce or expiry checks.

**Proofs:**

- `tests/posreadiness/pos_profile.go:1`

````
//go:build posreadiness
````

**Remediation:**

- Add nonce + TTL enforcement and tests for replay of POS intents and claimables.

## Funds Safety

Fund movements should guard against underflow and apply debits before credits.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:26`

````
// Staking / balances
	BalanceZNHB        *big.Int
	Stake              *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:31`

````
StakeLastPayoutTs  uint64
	LockedZNHB         *big.Int
	CollateralBalance  *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:90`

````
}
	if account.BalanceZNHB == nil {
		account.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:91`

````
if account.BalanceZNHB == nil {
		account.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:102`

````
}
	if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:103`

````
if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:120`

````
}
	if account.StakingRewards.AccruedZNHB == nil {
		account.StakingRewards.AccruedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:121`

````
if account.StakingRewards.AccruedZNHB == nil {
		account.StakingRewards.AccruedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:155`

````
BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(0),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:178`

````
if meta != nil {
		if meta.BalanceZNHB != nil {
			account.BalanceZNHB = new(big.Int).Set(meta.BalanceZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:179`

````
if meta.BalanceZNHB != nil {
			account.BalanceZNHB = new(big.Int).Set(meta.BalanceZNHB)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:190`

````
}
		if meta.LockedZNHB != nil {
			account.LockedZNHB = new(big.Int).Set(meta.LockedZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:191`

````
if meta.LockedZNHB != nil {
			account.LockedZNHB = new(big.Int).Set(meta.LockedZNHB)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:314`

````
// Staking / balances
		BalanceZNHB:        new(big.Int).Set(account.BalanceZNHB),
		Stake:              new(big.Int).Set(account.Stake),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:319`

````
StakeLastPayoutTs:  account.StakeLastPayoutTs,
		LockedZNHB:         new(big.Int).Set(account.LockedZNHB),
		CollateralBalance:  new(big.Int).Set(account.CollateralBalance),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:386`

````
// Staking / balances
		BalanceZNHB:        new(big.Int).Set(account.BalanceZNHB),
		Stake:              new(big.Int).Set(account.Stake),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:391`

````
StakeLastPayoutTs:  account.StakeLastPayoutTs,
		LockedZNHB:         new(big.Int).Set(account.LockedZNHB),
		CollateralBalance:  new(big.Int).Set(account.CollateralBalance),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:470`

````
meta := &accountMetadata{
		BalanceZNHB:    big.NewInt(0),
		Stake:          big.NewInt(0),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:474`

````
StakeLastIndex: big.NewInt(0),
		LockedZNHB:     big.NewInt(0),
		Unbonding:      make([]stakeUnbond, 0),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:483`

````
}
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:484`

````
if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:495`

````
}
	if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:496`

````
if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:526`

````
func (m *Manager) writeAccountMetadata(addr []byte, meta *accountMetadata) error {
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:527`

````
if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:532`

````
}
	if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/accounts.go:533`

````
if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:99`

````
if acc == nil {
		return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:107`

````
}
	if acc.BalanceZNHB != nil {
		cloned.BalanceZNHB = new(big.Int).Set(acc.BalanceZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:108`

````
if acc.BalanceZNHB != nil {
		cloned.BalanceZNHB = new(big.Int).Set(acc.BalanceZNHB)
	} else {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:110`

````
} else {
		cloned.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:225`

````
vaultAcc.BalanceNHB = new(big.Int).Add(vaultAcc.BalanceNHB, amt)
	case "ZNHB":
		if payerAcc.BalanceZNHB.Cmp(amt) < 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:226`

````
case "ZNHB":
		if payerAcc.BalanceZNHB.Cmp(amt) < 0 {
			return claimable.ErrInsufficientFunds
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:229`

````
}
		payerAcc.BalanceZNHB = new(big.Int).Sub(payerAcc.BalanceZNHB, amt)
		vaultAcc.BalanceZNHB = new(big.Int).Add(vaultAcc.BalanceZNHB, amt)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:230`

````
payerAcc.BalanceZNHB = new(big.Int).Sub(payerAcc.BalanceZNHB, amt)
		vaultAcc.BalanceZNHB = new(big.Int).Add(vaultAcc.BalanceZNHB, amt)
	default:
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:272`

````
recipientAcc.BalanceNHB = new(big.Int).Add(recipientAcc.BalanceNHB, amt)
	case "ZNHB":
		if vaultAcc.BalanceZNHB.Cmp(amt) < 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:273`

````
case "ZNHB":
		if vaultAcc.BalanceZNHB.Cmp(amt) < 0 {
			return claimable.ErrInsufficientFunds
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:276`

````
}
		vaultAcc.BalanceZNHB = new(big.Int).Sub(vaultAcc.BalanceZNHB, amt)
		recipientAcc.BalanceZNHB = new(big.Int).Add(recipientAcc.BalanceZNHB, amt)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/claimable.go:277`

````
vaultAcc.BalanceZNHB = new(big.Int).Sub(vaultAcc.BalanceZNHB, amt)
		recipientAcc.BalanceZNHB = new(big.Int).Add(recipientAcc.BalanceZNHB, amt)
	default:
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:11`

````
"nhbchain/native/fees"
)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:14`

````
type storedFeeCounter struct {
	Count           uint64
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:19`

````
type storedFeeMonthlyStatus struct {
	Window       string
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:27`

````
type storedFeeMonthlySnapshot struct {
	Window          string
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:35`

````
type storedFeeTotals struct {
	Domain string
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:40`

````
Gross  *big.Int
	Fee    *big.Int
	Net    *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:60`

````
func feeCounterKey(domain string, window time.Time, scope string, payer [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:61`

````
func feeCounterKey(domain string, window time.Time, scope string, payer [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
	month := monthKey(window)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:63`

````
month := monthKey(window)
	normalizedScope := fees.NormalizeFreeTierScope(scope)
	hexAddr := hex.EncodeToString(payer[:])
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:65`

````
hexAddr := hex.EncodeToString(payer[:])
	buf := make([]byte, len(feesCounterPrefix)+len(normalized)+1+len(month)+1+len(normalizedScope)+1+len(hexAddr))
	copy(buf, feesCounterPrefix)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:66`

````
buf := make([]byte, len(feesCounterPrefix)+len(normalized)+1+len(month)+1+len(normalizedScope)+1+len(hexAddr))
	copy(buf, feesCounterPrefix)
	offset := len(feesCounterPrefix)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:67`

````
copy(buf, feesCounterPrefix)
	offset := len(feesCounterPrefix)
	copy(buf[offset:], normalized)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:84`

````
func feeCounterLegacyKey(domain string, payer [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:85`

````
func feeCounterLegacyKey(domain string, payer [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
	hexAddr := hex.EncodeToString(payer[:])
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:87`

````
hexAddr := hex.EncodeToString(payer[:])
	buf := make([]byte, len(feesCounterPrefix)+len(normalized)+1+len(hexAddr))
	copy(buf, feesCounterPrefix)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:88`

````
buf := make([]byte, len(feesCounterPrefix)+len(normalized)+1+len(hexAddr))
	copy(buf, feesCounterPrefix)
	offset := len(feesCounterPrefix)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:89`

````
copy(buf, feesCounterPrefix)
	offset := len(feesCounterPrefix)
	copy(buf[offset:], normalized)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:107`

````
func feesMonthlyStatusKey() []byte {
	return []byte("fees/monthly/status")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:108`

````
func feesMonthlyStatusKey() []byte {
	return []byte("fees/monthly/status")
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:111`

````
func feesMonthlySnapshotKey(window string) []byte {
	trimmed := strings.TrimSpace(window)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:113`

````
trimmed := strings.TrimSpace(window)
	buf := make([]byte, len("fees/monthly/snapshot/")+len(trimmed))
	copy(buf, "fees/monthly/snapshot/")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:114`

````
buf := make([]byte, len("fees/monthly/snapshot/")+len(trimmed))
	copy(buf, "fees/monthly/snapshot/")
	copy(buf[len("fees/monthly/snapshot/"):], trimmed)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:115`

````
copy(buf, "fees/monthly/snapshot/")
	copy(buf[len("fees/monthly/snapshot/"):], trimmed)
	return buf
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:119`

````
// FeeMonthlyStatus captures the aggregate free-tier usage snapshot for the active UTC month.
type FeeMonthlyStatus struct {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:120`

````
// FeeMonthlyStatus captures the aggregate free-tier usage snapshot for the active UTC month.
type FeeMonthlyStatus struct {
	Window       string
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:129`

````
// FeeMonthlySnapshot stores a historical record of monthly usage captured during rollover.
type FeeMonthlySnapshot struct {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:130`

````
// FeeMonthlySnapshot stores a historical record of monthly usage captured during rollover.
type FeeMonthlySnapshot struct {
	Window      string
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:139`

````
func (stored *storedFeeMonthlyStatus) clone() *storedFeeMonthlyStatus {
	if stored == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:141`

````
if stored == nil {
		return &storedFeeMonthlyStatus{}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:147`

````
func (stored *storedFeeMonthlyStatus) toStatus() FeeMonthlyStatus {
	if stored == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:149`

````
if stored == nil {
		return FeeMonthlyStatus{}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:155`

````
}
	return FeeMonthlyStatus{
		Window:       stored.Window,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:165`

````
func (snapshot *storedFeeMonthlySnapshot) toSnapshot() (FeeMonthlySnapshot, bool) {
	if snapshot == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:167`

````
if snapshot == nil {
		return FeeMonthlySnapshot{}, false
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:177`

````
}
	return FeeMonthlySnapshot{
		Window:      snapshot.Window,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:187`

````
func feeTotalsKey(domain, asset string, wallet [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:188`

````
func feeTotalsKey(domain, asset string, wallet [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
	normalizedAsset := fees.NormalizeAsset(asset)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:189`

````
normalized := fees.NormalizeDomain(domain)
	normalizedAsset := fees.NormalizeAsset(asset)
	hexAddr := hex.EncodeToString(wallet[:])
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:191`

````
hexAddr := hex.EncodeToString(wallet[:])
	buf := make([]byte, len(feesTotalsPrefix)+len(normalized)+1+len(normalizedAsset)+1+len(hexAddr))
	copy(buf, feesTotalsPrefix)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:192`

````
buf := make([]byte, len(feesTotalsPrefix)+len(normalized)+1+len(normalizedAsset)+1+len(hexAddr))
	copy(buf, feesTotalsPrefix)
	offset := len(feesTotalsPrefix)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:193`

````
copy(buf, feesTotalsPrefix)
	offset := len(feesTotalsPrefix)
	copy(buf[offset:], normalized)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:206`

````
func feeTotalsIndexKey(domain string) []byte {
	normalized := fees.NormalizeDomain(domain)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:207`

````
func feeTotalsIndexKey(domain string) []byte {
	normalized := fees.NormalizeDomain(domain)
	buf := make([]byte, len(feesTotalsIndexPrefix)+len(normalized))
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:208`

````
normalized := fees.NormalizeDomain(domain)
	buf := make([]byte, len(feesTotalsIndexPrefix)+len(normalized))
	copy(buf, feesTotalsIndexPrefix)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:209`

````
buf := make([]byte, len(feesTotalsIndexPrefix)+len(normalized))
	copy(buf, feesTotalsIndexPrefix)
	copy(buf[len(feesTotalsIndexPrefix):], normalized)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:210`

````
copy(buf, feesTotalsIndexPrefix)
	copy(buf[len(feesTotalsIndexPrefix):], normalized)
	return buf
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:214`

````
func feeTotalsIndexEntry(asset string, wallet [20]byte) []byte {
	normalizedAsset := fees.NormalizeAsset(asset)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:215`

````
func feeTotalsIndexEntry(asset string, wallet [20]byte) []byte {
	normalizedAsset := fees.NormalizeAsset(asset)
	hexAddr := hex.EncodeToString(wallet[:])
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:224`

````
func parseFeeTotalsIndexEntry(raw []byte) (string, [20]byte, bool) {
	parts := bytes.SplitN(raw, []byte{'/'}, 2)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:238`

````
func (m *Manager) FeesGetCounter(domain string, payer [20]byte, window time.Time, scope string) (uint64, time.Time, bool, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:240`

````
if m == nil {
		return 0, time.Time{}, false, fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:244`

````
if normalizedWindow.IsZero() {
		return 0, time.Time{}, false, fmt.Errorf("fees: window start required")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:246`

````
}
	normalizedScope := fees.NormalizeFreeTierScope(scope)
	key := feeCounterKey(domain, normalizedWindow, normalizedScope, payer)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:247`

````
normalizedScope := fees.NormalizeFreeTierScope(scope)
	key := feeCounterKey(domain, normalizedWindow, normalizedScope, payer)
	var stored storedFeeCounter
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:248`

````
key := feeCounterKey(domain, normalizedWindow, normalizedScope, payer)
	var stored storedFeeCounter
	ok, err := m.KVGet(key, &stored)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:251`

````
if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("fees: load counter: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:260`

````
}
	if normalizedScope == fees.FreeTierScopeAggregate {
		legacyKey := feeCounterLegacyKey(domain, payer)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:261`

````
if normalizedScope == fees.FreeTierScopeAggregate {
		legacyKey := feeCounterLegacyKey(domain, payer)
		var legacy storedFeeCounter
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:262`

````
legacyKey := feeCounterLegacyKey(domain, payer)
		var legacy storedFeeCounter
		legacyOK, legacyErr := m.KVGet(legacyKey, &legacy)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:265`

````
if legacyErr != nil {
			return 0, time.Time{}, false, fmt.Errorf("fees: load legacy counter: %w", legacyErr)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:280`

````
func (m *Manager) FeesPutCounter(domain string, payer [20]byte, windowStart time.Time, scope string, count uint64) error {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:282`

````
if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:286`

````
if normalizedWindow.IsZero() {
		return fmt.Errorf("fees: window start required")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:288`

````
}
	normalizedScope := fees.NormalizeFreeTierScope(scope)
	key := feeCounterKey(domain, normalizedWindow, normalizedScope, payer)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:289`

````
normalizedScope := fees.NormalizeFreeTierScope(scope)
	key := feeCounterKey(domain, normalizedWindow, normalizedScope, payer)
	stored := storedFeeCounter{Count: count}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:290`

````
key := feeCounterKey(domain, normalizedWindow, normalizedScope, payer)
	stored := storedFeeCounter{Count: count}
	stored.WindowStartUnix = uint64(normalizedWindow.UTC().Unix())
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:293`

````
if err := m.KVPut(key, stored); err != nil {
		return fmt.Errorf("fees: persist counter: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:298`

````
func (m *Manager) feesLoadMonthlyStatus() (*storedFeeMonthlyStatus, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:300`

````
if m == nil {
		return &storedFeeMonthlyStatus{}, nil
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:302`

````
}
	key := feesMonthlyStatusKey()
	var stored storedFeeMonthlyStatus
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:303`

````
key := feesMonthlyStatusKey()
	var stored storedFeeMonthlyStatus
	ok, err := m.KVGet(key, &stored)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:306`

````
if err != nil {
		return nil, fmt.Errorf("fees: load monthly status: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:309`

````
if !ok {
		return &storedFeeMonthlyStatus{}, nil
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:314`

````
func (m *Manager) feesStoreMonthlyStatus(status *storedFeeMonthlyStatus) error {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:316`

````
if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:319`

````
if status == nil {
		status = &storedFeeMonthlyStatus{}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:323`

````
trimmedLast := strings.TrimSpace(status.LastRollover)
	stored := &storedFeeMonthlyStatus{
		Window:       trimmedWindow,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:330`

````
}
	return m.KVPut(feesMonthlyStatusKey(), stored)
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:333`

````
// FeesEnsureMonthlyRollover snapshots the previous month and resets the
// aggregate counters when the supplied timestamp enters a new UTC month.
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:335`

````
// aggregate counters when the supplied timestamp enters a new UTC month.
func (m *Manager) FeesEnsureMonthlyRollover(now time.Time) (FeeMonthlyStatus, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:337`

````
if m == nil {
		return FeeMonthlyStatus{}, fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:340`

````
if now.IsZero() {
		return FeeMonthlyStatus{}, fmt.Errorf("fees: rollover timestamp required")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:344`

````
if current == "000000" {
		return FeeMonthlyStatus{}, fmt.Errorf("fees: invalid rollover window")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:346`

````
}
	stored, err := m.feesLoadMonthlyStatus()
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:348`

````
if err != nil {
		return FeeMonthlyStatus{}, err
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:352`

````
stored.Window = current
		if err := m.feesStoreMonthlyStatus(stored); err != nil {
			return FeeMonthlyStatus{}, err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:353`

````
if err := m.feesStoreMonthlyStatus(stored); err != nil {
			return FeeMonthlyStatus{}, err
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:362`

````
if previous != "" {
		snapshot := &storedFeeMonthlySnapshot{
			Window:          previous,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:369`

````
}
		if err := m.KVPut(feesMonthlySnapshotKey(previous), snapshot); err != nil {
			return FeeMonthlyStatus{}, fmt.Errorf("fees: persist monthly snapshot: %w", err)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:370`

````
if err := m.KVPut(feesMonthlySnapshotKey(previous), snapshot); err != nil {
			return FeeMonthlyStatus{}, fmt.Errorf("fees: persist monthly snapshot: %w", err)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:378`

````
stored.Wallets = 0
	if err := m.feesStoreMonthlyStatus(stored); err != nil {
		return FeeMonthlyStatus{}, err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:379`

````
if err := m.feesStoreMonthlyStatus(stored); err != nil {
		return FeeMonthlyStatus{}, err
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:384`

````
// FeesMonthlyStatus returns the aggregate monthly free-tier usage snapshot.
func (m *Manager) FeesMonthlyStatus() (FeeMonthlyStatus, error) {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:385`

````
// FeesMonthlyStatus returns the aggregate monthly free-tier usage snapshot.
func (m *Manager) FeesMonthlyStatus() (FeeMonthlyStatus, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:387`

````
if m == nil {
		return FeeMonthlyStatus{}, fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:389`

````
}
	stored, err := m.feesLoadMonthlyStatus()
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:391`

````
if err != nil {
		return FeeMonthlyStatus{}, err
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:396`

````
// FeesMonthlySnapshot loads the stored snapshot for the supplied window, if present.
func (m *Manager) FeesMonthlySnapshot(window string) (FeeMonthlySnapshot, bool, error) {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:397`

````
// FeesMonthlySnapshot loads the stored snapshot for the supplied window, if present.
func (m *Manager) FeesMonthlySnapshot(window string) (FeeMonthlySnapshot, bool, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:399`

````
if m == nil {
		return FeeMonthlySnapshot{}, false, fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:403`

````
if trimmed == "" {
		return FeeMonthlySnapshot{}, false, fmt.Errorf("fees: snapshot window required")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:405`

````
}
	key := feesMonthlySnapshotKey(trimmed)
	var stored storedFeeMonthlySnapshot
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:406`

````
key := feesMonthlySnapshotKey(trimmed)
	var stored storedFeeMonthlySnapshot
	ok, err := m.KVGet(key, &stored)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:409`

````
if err != nil {
		return FeeMonthlySnapshot{}, false, fmt.Errorf("fees: load monthly snapshot: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:412`

````
if !ok {
		return FeeMonthlySnapshot{}, false, nil
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:418`

````
// FeesRecordUsage updates the aggregate monthly usage counters following a
// transaction that evaluated the free-tier policy.
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:420`

````
// transaction that evaluated the free-tier policy.
func (m *Manager) FeesRecordUsage(window time.Time, freeTierLimit uint64, counter uint64, freeTierApplied bool) error {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:422`

````
if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:426`

````
if normalized.IsZero() {
		return fmt.Errorf("fees: usage window required")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:430`

````
if month == "000000" {
		return fmt.Errorf("fees: invalid usage window")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:432`

````
}
	stored, err := m.feesLoadMonthlyStatus()
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:437`

````
if strings.TrimSpace(stored.Window) != month {
		if _, err := m.FeesEnsureMonthlyRollover(normalized); err != nil {
			return err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:440`

````
}
		stored, err = m.feesLoadMonthlyStatus()
		if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:446`

````
if strings.TrimSpace(stored.Window) != month {
		return fmt.Errorf("fees: monthly status mismatch")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:456`

````
}
	if err := m.feesStoreMonthlyStatus(updated); err != nil {
		return err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:462`

````
func ensureFeeTotalsDefaults(stored *storedFeeTotals) {
	if stored == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:469`

````
}
	if stored.Fee == nil {
		stored.Fee = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:470`

````
if stored.Fee == nil {
		stored.Fee = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:477`

````
func (stored *storedFeeTotals) toTotals() fees.Totals {
	ensureFeeTotalsDefaults(stored)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:478`

````
func (stored *storedFeeTotals) toTotals() fees.Totals {
	ensureFeeTotalsDefaults(stored)
	totals := fees.Totals{Domain: fees.NormalizeDomain(stored.Domain), Asset: fees.NormalizeAsset(stored.Asset), Wallet: stored.Wallet}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:479`

````
ensureFeeTotalsDefaults(stored)
	totals := fees.Totals{Domain: fees.NormalizeDomain(stored.Domain), Asset: fees.NormalizeAsset(stored.Asset), Wallet: stored.Wallet}
	if stored.Gross != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:483`

````
}
	if stored.Fee != nil {
		totals.Fee = new(big.Int).Set(stored.Fee)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:484`

````
if stored.Fee != nil {
		totals.Fee = new(big.Int).Set(stored.Fee)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:492`

````
func newStoredFeeTotals(record *fees.Totals) *storedFeeTotals {
	if record == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:494`

````
if record == nil {
		return &storedFeeTotals{}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:496`

````
}
	stored := &storedFeeTotals{Domain: fees.NormalizeDomain(record.Domain), Asset: fees.NormalizeAsset(record.Asset), Wallet: record.Wallet}
	if record.Gross != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:500`

````
}
	if record.Fee != nil {
		stored.Fee = new(big.Int).Set(record.Fee)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:501`

````
if record.Fee != nil {
		stored.Fee = new(big.Int).Set(record.Fee)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:506`

````
}
	ensureFeeTotalsDefaults(stored)
	return stored
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:510`

````
func (m *Manager) FeesGetTotals(domain, asset string, wallet [20]byte) (*fees.Totals, bool, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:512`

````
if m == nil {
		return nil, false, fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:514`

````
}
	key := feeTotalsKey(domain, asset, wallet)
	var stored storedFeeTotals
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:515`

````
key := feeTotalsKey(domain, asset, wallet)
	var stored storedFeeTotals
	ok, err := m.KVGet(key, &stored)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:518`

````
if err != nil {
		return nil, false, fmt.Errorf("fees: load totals: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:527`

````
func (m *Manager) FeesPutTotals(record *fees.Totals) error {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:529`

````
if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:532`

````
if record == nil {
		return fmt.Errorf("fees: totals record required")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:534`

````
}
	stored := newStoredFeeTotals(record)
	key := feeTotalsKey(record.Domain, record.Asset, record.Wallet)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:535`

````
stored := newStoredFeeTotals(record)
	key := feeTotalsKey(record.Domain, record.Asset, record.Wallet)
	if err := m.KVPut(key, stored); err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:537`

````
if err := m.KVPut(key, stored); err != nil {
		return fmt.Errorf("fees: persist totals: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:539`

````
}
	indexKey := feeTotalsIndexKey(record.Domain)
	var indexed [][]byte
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:542`

````
if err := m.KVGetList(indexKey, &indexed); err != nil {
		return fmt.Errorf("fees: load totals index: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:545`

````
found := false
	entry := feeTotalsIndexEntry(record.Asset, record.Wallet)
	for _, existing := range indexed {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:554`

````
if err := m.KVAppend(indexKey, append([]byte(nil), entry...)); err != nil {
			return fmt.Errorf("fees: update totals index: %w", err)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:570`

````
func (m *Manager) FeesAccumulateTotals(domain, asset string, wallet [20]byte, gross, fee, net *big.Int) error {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:572`

````
if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:574`

````
}
	record, ok, err := m.FeesGetTotals(domain, asset, wallet)
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:579`

````
if !ok {
		record = &fees.Totals{Domain: fees.NormalizeDomain(domain), Asset: fees.NormalizeAsset(asset), Wallet: wallet}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:582`

````
addToTotals(&record.Gross, gross)
	addToTotals(&record.Fee, fee)
	addToTotals(&record.Net, net)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:584`

````
addToTotals(&record.Net, net)
	return m.FeesPutTotals(record)
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:587`

````
func (m *Manager) FeesListTotals(domain string) ([]fees.Totals, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:589`

````
if m == nil {
		return nil, fmt.Errorf("fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:591`

````
}
	indexKey := feeTotalsIndexKey(domain)
	var entries [][]byte
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:594`

````
if err := m.KVGetList(indexKey, &entries); err != nil {
		return nil, fmt.Errorf("fees: load totals index: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:596`

````
}
	results := make([]fees.Totals, 0, len(entries))
	for _, raw := range entries {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:598`

````
for _, raw := range entries {
		asset, wallet, ok := parseFeeTotalsIndexEntry(raw)
		if !ok {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees.go:602`

````
}
		record, ok, err := m.FeesGetTotals(domain, asset, wallet)
		if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:10`

````
const rollingFeesDateFormat = "20060102"
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:13`

````
var (
	feesDayPrefixBytes   = []byte("fees/day/")
	feesDayIndexKeyBytes = []byte("fees/day/index")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:14`

````
feesDayPrefixBytes   = []byte("fees/day/")
	feesDayIndexKeyBytes = []byte("fees/day/index")
)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:17`

````
type RollingFees struct {
	manager *Manager
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:21`

````
func NewRollingFees(manager *Manager) *RollingFees {
	return &RollingFees{manager: manager}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:22`

````
func NewRollingFees(manager *Manager) *RollingFees {
	return &RollingFees{manager: manager}
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:25`

````
type storedRollingFees struct {
	NetNHB  *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:27`

````
NetNHB  *big.Int
	NetZNHB *big.Int
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:30`

````
func (r *RollingFees) AddDay(tsDay time.Time, netFeesNHB, netFeesZNHB *big.Int) error {
	if r == nil || r.manager == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:32`

````
if r == nil || r.manager == nil {
		return fmt.Errorf("rolling fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:35`

````
if tsDay.IsZero() {
		return fmt.Errorf("rolling fees: day timestamp required")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:38`

````
day := dayStartUTC(tsDay)
	dayID := day.Format(rollingFeesDateFormat)
	key := rollingFeeBucketKey(dayID)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:39`

````
dayID := day.Format(rollingFeesDateFormat)
	key := rollingFeeBucketKey(dayID)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:41`

````
var stored storedRollingFees
	ok, err := r.manager.KVGet(key, &stored)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:44`

````
if err != nil {
		return fmt.Errorf("rolling fees: load bucket: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:48`

````
stored.NetNHB = big.NewInt(0)
		stored.NetZNHB = big.NewInt(0)
	} else {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:53`

````
}
		if stored.NetZNHB == nil {
			stored.NetZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:54`

````
if stored.NetZNHB == nil {
			stored.NetZNHB = big.NewInt(0)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:58`

````
if netFeesNHB != nil {
		stored.NetNHB = new(big.Int).Add(stored.NetNHB, netFeesNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:59`

````
if netFeesNHB != nil {
		stored.NetNHB = new(big.Int).Add(stored.NetNHB, netFeesNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:61`

````
}
	if netFeesZNHB != nil {
		stored.NetZNHB = new(big.Int).Add(stored.NetZNHB, netFeesZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:62`

````
if netFeesZNHB != nil {
		stored.NetZNHB = new(big.Int).Add(stored.NetZNHB, netFeesZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:66`

````
if err := r.manager.KVPut(key, stored); err != nil {
		return fmt.Errorf("rolling fees: persist bucket: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:75`

````
func (r *RollingFees) Get7dNetFeesNHB(tsNow time.Time) (*big.Int, error) {
	return r.sumWindow(tsNow, func(stored *storedRollingFees) *big.Int {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:76`

````
func (r *RollingFees) Get7dNetFeesNHB(tsNow time.Time) (*big.Int, error) {
	return r.sumWindow(tsNow, func(stored *storedRollingFees) *big.Int {
		return stored.NetNHB
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:81`

````
func (r *RollingFees) Get7dNetFeesZNHB(tsNow time.Time) (*big.Int, error) {
	return r.sumWindow(tsNow, func(stored *storedRollingFees) *big.Int {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:82`

````
func (r *RollingFees) Get7dNetFeesZNHB(tsNow time.Time) (*big.Int, error) {
	return r.sumWindow(tsNow, func(stored *storedRollingFees) *big.Int {
		return stored.NetZNHB
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:83`

````
return r.sumWindow(tsNow, func(stored *storedRollingFees) *big.Int {
		return stored.NetZNHB
	})
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:87`

````
func (r *RollingFees) sumWindow(tsNow time.Time, selector func(*storedRollingFees) *big.Int) (*big.Int, error) {
	if r == nil || r.manager == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:89`

````
if r == nil || r.manager == nil {
		return nil, fmt.Errorf("rolling fees: state manager not initialised")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:98`

````
var index []string
	if err := r.manager.KVGetList(rollingFeeIndexKey(), &index); err != nil {
		return nil, fmt.Errorf("rolling fees: load index: %w", err)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:99`

````
if err := r.manager.KVGetList(rollingFeeIndexKey(), &index); err != nil {
		return nil, fmt.Errorf("rolling fees: load index: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:104`

````
for _, dayID := range index {
		day, err := parseRollingFeesDay(dayID)
		if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:106`

````
if err != nil {
			return nil, fmt.Errorf("rolling fees: parse index entry %q: %w", dayID, err)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:111`

````
}
		key := rollingFeeBucketKey(dayID)
		var stored storedRollingFees
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:112`

````
key := rollingFeeBucketKey(dayID)
		var stored storedRollingFees
		ok, err := r.manager.KVGet(key, &stored)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:115`

````
if err != nil {
			return nil, fmt.Errorf("rolling fees: load bucket %q: %w", dayID, err)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:129`

````
func (r *RollingFees) updateIndex(dayID string) error {
	var index []string
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:131`

````
var index []string
	if err := r.manager.KVGetList(rollingFeeIndexKey(), &index); err != nil {
		return fmt.Errorf("rolling fees: load index: %w", err)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:132`

````
if err := r.manager.KVGetList(rollingFeeIndexKey(), &index); err != nil {
		return fmt.Errorf("rolling fees: load index: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:146`

````
}
	if err := r.manager.KVPut(rollingFeeIndexKey(), filtered); err != nil {
		return fmt.Errorf("rolling fees: persist index: %w", err)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:147`

````
if err := r.manager.KVPut(rollingFeeIndexKey(), filtered); err != nil {
		return fmt.Errorf("rolling fees: persist index: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:152`

````
func rollingFeeBucketKey(dayID string) []byte {
	key := make([]byte, len(feesDayPrefixBytes)+len(dayID))
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:153`

````
func rollingFeeBucketKey(dayID string) []byte {
	key := make([]byte, len(feesDayPrefixBytes)+len(dayID))
	copy(key, feesDayPrefixBytes)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:154`

````
key := make([]byte, len(feesDayPrefixBytes)+len(dayID))
	copy(key, feesDayPrefixBytes)
	copy(key[len(feesDayPrefixBytes):], dayID)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:155`

````
copy(key, feesDayPrefixBytes)
	copy(key[len(feesDayPrefixBytes):], dayID)
	return key
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:159`

````
func rollingFeeIndexKey() []byte {
	return append([]byte(nil), feesDayIndexKeyBytes...)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:160`

````
func rollingFeeIndexKey() []byte {
	return append([]byte(nil), feesDayIndexKeyBytes...)
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:163`

````
func parseRollingFeesDay(dayID string) (time.Time, error) {
	if len(dayID) != len(rollingFeesDateFormat) {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:164`

````
func parseRollingFeesDay(dayID string) (time.Time, error) {
	if len(dayID) != len(rollingFeesDateFormat) {
		return time.Time{}, fmt.Errorf("invalid day format")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/fees_rolling.go:167`

````
}
	return time.ParseInLocation(rollingFeesDateFormat, dayID, time.UTC)
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:11`

````
var (
	znhbWeiRat = big.NewRat(1000000000000000000, 1)
)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:14`

````
func CalcDailyBudgetZNHB(now time.Time, rolling7dFeesNHB, rolling7dFeesZNHB *big.Int, price *big.Rat, cfg *loyalty.DynamicConfig) *big.Int {
	_ = now
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:23`

````
capBps := cfg.DailyCapPctOf7dFeesBps
	if capBps > loyalty.BaseRewardBpsDenominator {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:37`

````
if capBps > 0 {
		feesZNHB := ratFromWei(rolling7dFeesZNHB)
		feesNHB := ratFromWei(rolling7dFeesNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:38`

````
feesZNHB := ratFromWei(rolling7dFeesZNHB)
		feesNHB := ratFromWei(rolling7dFeesNHB)
		if feesNHB.Sign() > 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:39`

````
feesNHB := ratFromWei(rolling7dFeesNHB)
		if feesNHB.Sign() > 0 {
			feesNHB.Quo(feesNHB, price)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:40`

````
if feesNHB.Sign() > 0 {
			feesNHB.Quo(feesNHB, price)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:42`

````
}
		totalFees := new(big.Rat).Add(feesZNHB, feesNHB)
		if totalFees.Sign() > 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:43`

````
totalFees := new(big.Rat).Add(feesZNHB, feesNHB)
		if totalFees.Sign() > 0 {
			pct := new(big.Rat).SetUint64(uint64(capBps))
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:46`

````
pct.Quo(pct, big.NewRat(loyalty.BaseRewardBpsDenominator, 1))
			totalFees.Mul(totalFees, pct)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:48`

````
}
		budgets = append(budgets, ratToWei(totalFees))
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:74`

````
r := new(big.Rat).SetInt(amount)
	return r.Quo(r, znhbWeiRat)
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_budget.go:81`

````
}
	scaled := new(big.Rat).Mul(value, znhbWeiRat)
	num := new(big.Int).Set(scaled.Num())
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:25`

````
}
	if reward.AmountZNHB == nil || reward.AmountZNHB.Sign() <= 0 {
		return
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:29`

````
copied := reward
	copied.AmountZNHB = new(big.Int).Set(reward.AmountZNHB)
	*p = append(*p, copied)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:41`

````
// SumPending returns the aggregate ZNHB amount across all queued rewards.
func (p PendingRewards) SumPending() *big.Int {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:45`

````
for i := range p {
		if p[i].AmountZNHB == nil {
			continue
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:48`

````
}
		total.Add(total, p[i].AmountZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:54`

````
type loyaltyDaySnapshot struct {
	PaidZNHB          *big.Int
	TotalProposedZNHB *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:55`

````
PaidZNHB          *big.Int
	TotalProposedZNHB *big.Int
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:64`

````
if s == nil {
		return &loyaltyDaySnapshot{PaidZNHB: big.NewInt(0), TotalProposedZNHB: big.NewInt(0)}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:66`

````
}
	if s.PaidZNHB == nil {
		s.PaidZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:67`

````
if s.PaidZNHB == nil {
		s.PaidZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:69`

````
}
	if s.PaidZNHB.Sign() < 0 {
		s.PaidZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:70`

````
if s.PaidZNHB.Sign() < 0 {
		s.PaidZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:72`

````
}
	if s.TotalProposedZNHB == nil {
		s.TotalProposedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:73`

````
if s.TotalProposedZNHB == nil {
		s.TotalProposedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:75`

````
}
	if s.TotalProposedZNHB.Sign() < 0 {
		s.TotalProposedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:76`

````
if s.TotalProposedZNHB.Sign() < 0 {
		s.TotalProposedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:128`

````
// AddProposedTodayZNHB increments the running total of proposed base rewards
// for the supplied UTC day. The updated total is returned.
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:130`

````
// for the supplied UTC day. The updated total is returned.
func (m *Manager) AddProposedTodayZNHB(now time.Time, amount *big.Int) (*big.Int, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:139`

````
}
		return new(big.Int).Set(snapshot.TotalProposedZNHB), nil
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:146`

````
}
	snapshot.TotalProposedZNHB = new(big.Int).Add(snapshot.TotalProposedZNHB, amount)
	if err := m.storeLoyaltyDaySnapshot(day, snapshot); err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:150`

````
}
	return new(big.Int).Set(snapshot.TotalProposedZNHB), nil
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:153`

````
// AddPaidTodayZNHB increments the paid total for the supplied UTC day and
// returns the new cumulative sum.
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:155`

````
// returns the new cumulative sum.
func (m *Manager) AddPaidTodayZNHB(now time.Time, amount *big.Int) (*big.Int, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:164`

````
}
		return new(big.Int).Set(snapshot.PaidZNHB), nil
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:171`

````
}
	snapshot.PaidZNHB = new(big.Int).Add(snapshot.PaidZNHB, amount)
	if err := m.storeLoyaltyDaySnapshot(day, snapshot); err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:175`

````
}
	return new(big.Int).Set(snapshot.PaidZNHB), nil
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:178`

````
// GetRemainingDailyBudgetZNHB resolves the remaining daily base reward budget
// expressed in ZNHB for the supplied timestamp. When price guard checks fail or
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:179`

````
// GetRemainingDailyBudgetZNHB resolves the remaining daily base reward budget
// expressed in ZNHB for the supplied timestamp. When price guard checks fail or
// configuration is missing the function returns zero without error.
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:181`

````
// configuration is missing the function returns zero without error.
func (m *Manager) GetRemainingDailyBudgetZNHB(now time.Time) (*big.Int, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:206`

````
tracker := NewRollingFees(m)
	feesNHB, err := tracker.Get7dNetFeesNHB(now)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:207`

````
tracker := NewRollingFees(m)
	feesNHB, err := tracker.Get7dNetFeesNHB(now)
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:211`

````
}
	feesZNHB, err := tracker.Get7dNetFeesZNHB(now)
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:216`

````
budget := CalcDailyBudgetZNHB(now, feesNHB, feesZNHB, price, &normalized.Dynamic)
	remaining := new(big.Int).Sub(budget, snapshot.PaidZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:217`

````
budget := CalcDailyBudgetZNHB(now, feesNHB, feesZNHB, price, &normalized.Dynamic)
	remaining := new(big.Int).Sub(budget, snapshot.PaidZNHB)
	if remaining.Sign() < 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:232`

````
parts := strings.Split(pair, "/")
	base := "ZNHB"
	if len(parts) > 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:262`

````
Recipient  [20]byte
	AmountZNHB *big.Int
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:275`

````
SmoothingStepBps uint32
	YtdEmissionsZNHB *big.Int
	YearlyCapZNHB    *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:276`

````
YtdEmissionsZNHB *big.Int
	YearlyCapZNHB    *big.Int
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:285`

````
clone := *s
	if s.YtdEmissionsZNHB != nil {
		clone.YtdEmissionsZNHB = new(big.Int).Set(s.YtdEmissionsZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:286`

````
if s.YtdEmissionsZNHB != nil {
		clone.YtdEmissionsZNHB = new(big.Int).Set(s.YtdEmissionsZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:288`

````
}
	if s.YearlyCapZNHB != nil {
		clone.YearlyCapZNHB = new(big.Int).Set(s.YearlyCapZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:289`

````
if s.YearlyCapZNHB != nil {
		clone.YearlyCapZNHB = new(big.Int).Set(s.YearlyCapZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:318`

````
}
	if s.YtdEmissionsZNHB == nil {
		s.YtdEmissionsZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:319`

````
if s.YtdEmissionsZNHB == nil {
		s.YtdEmissionsZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:321`

````
}
	if s.YearlyCapZNHB == nil {
		s.YearlyCapZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:322`

````
if s.YearlyCapZNHB == nil {
		s.YearlyCapZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:324`

````
}
	if s.YtdEmissionsZNHB.Sign() < 0 {
		s.YtdEmissionsZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:325`

````
if s.YtdEmissionsZNHB.Sign() < 0 {
		s.YtdEmissionsZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:327`

````
}
	if s.YearlyCapZNHB.Sign() < 0 {
		s.YearlyCapZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:328`

````
if s.YearlyCapZNHB.Sign() < 0 {
		s.YearlyCapZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:330`

````
}
	if s.YearlyCapZNHB.Sign() > 0 && s.YtdEmissionsZNHB.Cmp(s.YearlyCapZNHB) > 0 {
		s.YtdEmissionsZNHB = new(big.Int).Set(s.YearlyCapZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:331`

````
if s.YearlyCapZNHB.Sign() > 0 && s.YtdEmissionsZNHB.Cmp(s.YearlyCapZNHB) > 0 {
		s.YtdEmissionsZNHB = new(big.Int).Set(s.YearlyCapZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:348`

````
if amount == nil || amount.Sign() <= 0 {
		return true, s.YearlyCapZNHB.Sign() > 0 && s.YtdEmissionsZNHB.Cmp(s.YearlyCapZNHB) == 0
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:350`

````
}
	if s.YearlyCapZNHB.Sign() <= 0 {
		s.YtdEmissionsZNHB = new(big.Int).Add(s.YtdEmissionsZNHB, amount)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:351`

````
if s.YearlyCapZNHB.Sign() <= 0 {
		s.YtdEmissionsZNHB = new(big.Int).Add(s.YtdEmissionsZNHB, amount)
		return true, false
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:354`

````
}
	projected := new(big.Int).Add(s.YtdEmissionsZNHB, amount)
	cmp := projected.Cmp(s.YearlyCapZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:355`

````
projected := new(big.Int).Add(s.YtdEmissionsZNHB, amount)
	cmp := projected.Cmp(s.YearlyCapZNHB)
	if cmp > 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:359`

````
}
	s.YtdEmissionsZNHB = projected
	return true, cmp == 0
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:427`

````
SmoothingStepBps: normalized.SmoothingStepBps,
		YtdEmissionsZNHB: big.NewInt(0),
		YearlyCapZNHB:    big.NewInt(0),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/loyalty_engine.go:428`

````
YtdEmissionsZNHB: big.NewInt(0),
		YearlyCapZNHB:    big.NewInt(0),
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:120`

````
lendingMarketPrefix            = []byte("lending/market/")
	lendingFeeAccrualPrefix        = []byte("lending/fees/")
	lendingUserPrefix              = []byte("lending/user/")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:123`

````
lendingPoolIndexKey            = []byte("lending/pools/index")
	feesCounterPrefix              = []byte("fees/counter/")
	feesTotalsPrefix               = []byte("fees/totals/")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:124`

````
feesCounterPrefix              = []byte("fees/counter/")
	feesTotalsPrefix               = []byte("fees/totals/")
	feesTotalsIndexPrefix          = []byte("fees/totals/index/")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:125`

````
feesTotalsPrefix               = []byte("fees/totals/")
	feesTotalsIndexPrefix          = []byte("fees/totals/index/")
)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:241`

````
rewards := &types.StakingRewards{
		AccruedZNHB: big.NewInt(0),
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:247`

````
rewards.LastPayoutUnix = snap.LastPayoutUnix
	if snap.AccruedZNHB != nil {
		rewards.AccruedZNHB = new(big.Int).Set(snap.AccruedZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:248`

````
if snap.AccruedZNHB != nil {
		rewards.AccruedZNHB = new(big.Int).Set(snap.AccruedZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:264`

````
snap := &AccountSnap{LastPayoutUnix: rewards.LastPayoutUnix}
	if rewards.AccruedZNHB != nil {
		snap.AccruedZNHB = new(big.Int).Set(rewards.AccruedZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:265`

````
if rewards.AccruedZNHB != nil {
		snap.AccruedZNHB = new(big.Int).Set(rewards.AccruedZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:364`

````
// GovernanceEscrowKey resolves the deposit escrow bucket for a governance
// participant. Escrow balances are denominated in ZNHB and tracked per account.
func GovernanceEscrowKey(addr []byte) []byte {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1110`

````
func lendingFeeAccrualKey(poolID string) []byte {
	trimmed := strings.TrimSpace(poolID)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1112`

````
trimmed := strings.TrimSpace(poolID)
	buf := make([]byte, len(lendingFeeAccrualPrefix)+len(trimmed))
	copy(buf, lendingFeeAccrualPrefix)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1113`

````
buf := make([]byte, len(lendingFeeAccrualPrefix)+len(trimmed))
	copy(buf, lendingFeeAccrualPrefix)
	copy(buf[len(lendingFeeAccrualPrefix):], trimmed)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1114`

````
copy(buf, lendingFeeAccrualPrefix)
	copy(buf[len(lendingFeeAccrualPrefix):], trimmed)
	return buf
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1140`

````
DeveloperCollector [20]byte
	DeveloperFeeBps    uint64
	TotalNHBSupplied   *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1150`

````
type storedLendingFees struct {
	ProtocolFeesWei  *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1151`

````
type storedLendingFees struct {
	ProtocolFeesWei  *big.Int
	DeveloperFeesWei *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1152`

````
ProtocolFeesWei  *big.Int
	DeveloperFeesWei *big.Int
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1163`

````
ReserveFactor:   market.ReserveFactor,
		DeveloperFeeBps: market.DeveloperFeeBps,
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1168`

````
}
	if market.DeveloperFeeCollector.Bytes() != nil {
		copy(stored.DeveloperCollector[:], market.DeveloperFeeCollector.Bytes())
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1169`

````
if market.DeveloperFeeCollector.Bytes() != nil {
		copy(stored.DeveloperCollector[:], market.DeveloperFeeCollector.Bytes())
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1197`

````
ReserveFactor:   s.ReserveFactor,
		DeveloperFeeBps: s.DeveloperFeeBps,
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1204`

````
if !bytes.Equal(s.DeveloperCollector[:], zeroAddr[:]) {
		market.DeveloperFeeCollector = crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), s.DeveloperCollector[:]...))
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1224`

````
func newStoredLendingFees(fees *lending.FeeAccrual) *storedLendingFees {
	if fees == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1225`

````
func newStoredLendingFees(fees *lending.FeeAccrual) *storedLendingFees {
	if fees == nil {
		return nil
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1228`

````
}
	stored := &storedLendingFees{}
	if fees.ProtocolFeesWei != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1229`

````
stored := &storedLendingFees{}
	if fees.ProtocolFeesWei != nil {
		stored.ProtocolFeesWei = new(big.Int).Set(fees.ProtocolFeesWei)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1230`

````
if fees.ProtocolFeesWei != nil {
		stored.ProtocolFeesWei = new(big.Int).Set(fees.ProtocolFeesWei)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1232`

````
}
	if fees.DeveloperFeesWei != nil {
		stored.DeveloperFeesWei = new(big.Int).Set(fees.DeveloperFeesWei)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1233`

````
if fees.DeveloperFeesWei != nil {
		stored.DeveloperFeesWei = new(big.Int).Set(fees.DeveloperFeesWei)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1238`

````
func (s *storedLendingFees) toFeeAccrual() *lending.FeeAccrual {
	if s == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1242`

````
}
	fees := &lending.FeeAccrual{}
	if s.ProtocolFeesWei != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1243`

````
fees := &lending.FeeAccrual{}
	if s.ProtocolFeesWei != nil {
		fees.ProtocolFeesWei = new(big.Int).Set(s.ProtocolFeesWei)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1244`

````
if s.ProtocolFeesWei != nil {
		fees.ProtocolFeesWei = new(big.Int).Set(s.ProtocolFeesWei)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1246`

````
}
	if s.DeveloperFeesWei != nil {
		fees.DeveloperFeesWei = new(big.Int).Set(s.DeveloperFeesWei)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1247`

````
if s.DeveloperFeesWei != nil {
		fees.DeveloperFeesWei = new(big.Int).Set(s.DeveloperFeesWei)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1249`

````
}
	return fees
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1254`

````
Address        [20]byte
	CollateralZNHB *big.Int
	SupplyShares   *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1266`

````
copy(stored.Address[:], account.Address.Bytes())
	if account.CollateralZNHB != nil {
		stored.CollateralZNHB = new(big.Int).Set(account.CollateralZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1267`

````
if account.CollateralZNHB != nil {
		stored.CollateralZNHB = new(big.Int).Set(account.CollateralZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1288`

````
}
	if s.CollateralZNHB != nil {
		account.CollateralZNHB = new(big.Int).Set(s.CollateralZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1289`

````
if s.CollateralZNHB != nil {
		account.CollateralZNHB = new(big.Int).Set(s.CollateralZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1411`

````
// LendingGetFeeAccrual loads the current lending fee accrual totals if present
// for the supplied pool identifier.
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1413`

````
// for the supplied pool identifier.
func (m *Manager) LendingGetFeeAccrual(poolID string) (*lending.FeeAccrual, bool, error) {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1421`

````
}
	var stored storedLendingFees
	ok, err := m.KVGet(lendingFeeAccrualKey(normalized), &stored)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1422`

````
var stored storedLendingFees
	ok, err := m.KVGet(lendingFeeAccrualKey(normalized), &stored)
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1429`

````
}
	return stored.toFeeAccrual(), true, nil
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1432`

````
// LendingPutFeeAccrual persists the provided lending fee accrual snapshot for
// the supplied pool.
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1434`

````
// the supplied pool.
func (m *Manager) LendingPutFeeAccrual(poolID string, fees *lending.FeeAccrual) error {
	if m == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1438`

````
}
	if fees == nil {
		return fmt.Errorf("lending: fee accrual must not be nil")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1439`

````
if fees == nil {
		return fmt.Errorf("lending: fee accrual must not be nil")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:1445`

````
}
	return m.KVPut(lendingFeeAccrualKey(normalized), newStoredLendingFees(fees))
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:2687`

````
Amount       *big.Int
	FeeBps       uint32
	Deadline     *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:2786`

````
Amount:       amount,
		FeeBps:       e.FeeBps,
		Deadline:     deadline,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/manager.go:2813`

````
}(),
		FeeBps:         s.FeeBps,
		MetaHash:       s.MetaHash,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_keys.go:21`

````
LastIndexUQ128x128 []byte
	AccruedZNHB        *big.Int
	LastPayoutUnix     int64
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_keys.go:67`

````
LastIndexUQ128x128 []byte
	AccruedZNHB        *big.Int
	LastPayoutUnix     uint64
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_keys.go:82`

````
LastPayoutUnix:     uint64(ts),
		AccruedZNHB:        big.NewInt(0),
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_keys.go:84`

````
}
	if snap.AccruedZNHB != nil {
		stored.AccruedZNHB = new(big.Int).Set(snap.AccruedZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_keys.go:85`

````
if snap.AccruedZNHB != nil {
		stored.AccruedZNHB = new(big.Int).Set(snap.AccruedZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_keys.go:92`

````
if s == nil {
		return &AccountSnap{AccruedZNHB: big.NewInt(0)}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_keys.go:97`

````
LastPayoutUnix:     int64(s.LastPayoutUnix),
		AccruedZNHB:        big.NewInt(0),
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_keys.go:99`

````
}
	if s.AccruedZNHB != nil {
		snap.AccruedZNHB = new(big.Int).Set(s.AccruedZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_keys.go:100`

````
if s.AccruedZNHB != nil {
		snap.AccruedZNHB = new(big.Int).Set(s.AccruedZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:86`

````
return events.StakeCapHit{
		RequestedZNHB: e.Requested(),
		AllowedZNHB:   e.Allowed(),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:87`

````
RequestedZNHB: e.Requested(),
		AllowedZNHB:   e.Allowed(),
		YTD:           e.YTD(),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:198`

````
if snap == nil {
		snap = &types.StakingRewards{AccruedZNHB: big.NewInt(0)}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:218`

````
}
		if account != nil && account.LockedZNHB != nil && account.LockedZNHB.Sign() > 0 {
			reward := new(big.Int).Mul(delta, account.LockedZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:219`

````
if account != nil && account.LockedZNHB != nil && account.LockedZNHB.Sign() > 0 {
			reward := new(big.Int).Mul(delta, account.LockedZNHB)
			reward.Quo(reward, uq128Unit)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:221`

````
reward.Quo(reward, uq128Unit)
			snap.AccruedZNHB.Add(snap.AccruedZNHB, reward)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:257`

````
}
	if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:258`

````
if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:261`

````
delta := new(big.Int).Set(amount)
	account.LockedZNHB.Add(account.LockedZNHB, delta)
	return e.mgr.PutAccountMetadata(addr, account)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:293`

````
}
	if account.LockedZNHB == nil || account.LockedZNHB.Sign() == 0 {
		return fmt.Errorf("insufficient stake")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:296`

````
}
	if account.LockedZNHB.Cmp(amount) < 0 {
		return fmt.Errorf("insufficient stake")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:300`

````
delta := new(big.Int).Set(amount)
	account.LockedZNHB.Sub(account.LockedZNHB, delta)
	return e.mgr.PutAccountMetadata(addr, account)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:337`

````
if snap == nil {
		snap = &types.StakingRewards{AccruedZNHB: big.NewInt(0)}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:398`

````
stakeBalance := big.NewInt(0)
	if account != nil && account.LockedZNHB != nil {
		stakeBalance = new(big.Int).Set(account.LockedZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:399`

````
if account != nil && account.LockedZNHB != nil {
		stakeBalance = new(big.Int).Set(account.LockedZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:413`

````
accrued := big.NewInt(0)
	if snap.AccruedZNHB != nil {
		accrued = new(big.Int).Set(snap.AccruedZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:414`

````
if snap.AccruedZNHB != nil {
		accrued = new(big.Int).Set(snap.AccruedZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:452`

````
snap.AccruedZNHB.Sub(snap.AccruedZNHB, payout)
	if snap.AccruedZNHB.Sign() < 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:453`

````
snap.AccruedZNHB.Sub(snap.AccruedZNHB, payout)
	if snap.AccruedZNHB.Sign() < 0 {
		snap.AccruedZNHB.SetInt64(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:454`

````
if snap.AccruedZNHB.Sign() < 0 {
		snap.AccruedZNHB.SetInt64(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state/staking_rewards.go:464`

````
if payout.Sign() > 0 {
		account.BalanceZNHB.Add(account.BalanceZNHB, payout)
		metrics.RecordRewardsPaid(payout)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:27`

````
"nhbchain/native/escrow"
	"nhbchain/native/fees"
	"nhbchain/native/governance"
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:69`

````
ErrTransferNHBPaused  = errors.New("nhb transfer: paused")
	ErrTransferZNHBPaused = errors.New("znhb transfer: paused")
	// ErrStakePaused indicates governance has paused staking mutations.
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:104`

````
pauses                nativecommon.PauseView
	escrowFeeTreasury     [20]byte
	usernameToAddr        map[string][]byte
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:128`

````
intentTTL             time.Duration
	feePolicy             fees.Policy
	blockCtx              BlockCtx
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:161`

````
paymasterLimits:       PaymasterLimits{},
		paymasterTopUp:        PaymasterAutoTopUpPolicy{Token: "ZNHB"},
		quotaConfig:           make(map[string]nativecommon.Quota),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:164`

````
intentTTL:             defaultIntentTTL,
		feePolicy:             fees.Policy{Domains: map[string]fees.DomainPolicy{}},
		blockCtx:              BlockCtx{},
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:264`

````
// SetFeePolicy updates the fee policy applied to eligible transactions.
func (sp *StateProcessor) SetFeePolicy(policy fees.Policy) {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:265`

````
// SetFeePolicy updates the fee policy applied to eligible transactions.
func (sp *StateProcessor) SetFeePolicy(policy fees.Policy) {
	if sp == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:271`

````
if clone.Domains == nil {
		clone.Domains = make(map[string]fees.DomainPolicy)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:273`

````
}
	sp.feePolicy = clone
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:276`

````
// FeePolicy returns a copy of the currently configured fee policy.
func (sp *StateProcessor) FeePolicy() fees.Policy {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:277`

````
// FeePolicy returns a copy of the currently configured fee policy.
func (sp *StateProcessor) FeePolicy() fees.Policy {
	if sp == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:279`

````
if sp == nil {
		return fees.Policy{}
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:281`

````
}
	return sp.feePolicy.Clone()
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:297`

````
func (sp *StateProcessor) applyTransactionFee(tx *types.Transaction, sender []byte, fromAcc, toAcc *types.Account) error {
	if sp == nil || tx == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:304`

````
case types.TxTypeTransfer:
		asset = fees.AssetNHB
	case types.TxTypeTransferZNHB:
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:305`

````
asset = fees.AssetNHB
	case types.TxTypeTransferZNHB:
		asset = fees.AssetZNHB
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:306`

````
case types.TxTypeTransferZNHB:
		asset = fees.AssetZNHB
	default:
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:314`

````
}
	cfg, ok := sp.feePolicy.DomainConfig(domain)
	if !ok {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:329`

````
now := sp.blockTimestamp()
	currentWindow := feeWindowStart(now)
	scope := cfg.FreeTierScope(asset)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:331`

````
scope := cfg.FreeTierScope(asset)
	counter, windowStart, _, err := manager.FeesGetCounter(domain, payer, currentWindow, scope)
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:335`

````
}
	if windowStart.IsZero() || !sameFeeWindow(windowStart, currentWindow) {
		windowStart = currentWindow
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:339`

````
}
	result := fees.Apply(fees.ApplyInput{
		Domain:        domain,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:343`

````
UsageCount:    counter,
		PolicyVersion: sp.feePolicy.Version,
		Config:        cfg,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:351`

````
}
	if err := manager.FeesPutCounter(domain, payer, result.WindowStart, scope, result.Counter); err != nil {
		return err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:354`

````
}
	if err := manager.FeesRecordUsage(result.WindowStart, cfg.FreeTierTxPerMonth, result.Counter, result.FreeTierApplied); err != nil {
		return err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:357`

````
}
	if err := manager.FeesAccumulateTotals(domain, result.Asset, result.OwnerWallet, gross, result.Fee, result.Net); err != nil {
		return err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:360`

````
}
	if result.Fee != nil && result.Fee.Sign() > 0 {
		if isZeroAddress(result.OwnerWallet) {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:362`

````
if isZeroAddress(result.OwnerWallet) {
			return fmt.Errorf("fees: missing route wallet for asset %s", result.Asset)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:364`

````
}
		routed := new(big.Int).Set(result.Fee)
		deducted := false
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:367`

````
switch result.Asset {
		case fees.AssetNHB:
			if toAcc != nil && toAcc.BalanceNHB != nil && toAcc.BalanceNHB.Cmp(routed) >= 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:376`

````
}
		case fees.AssetZNHB:
			if toAcc != nil && toAcc.BalanceZNHB != nil && toAcc.BalanceZNHB.Cmp(routed) >= 0 {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:378`

````
if toAcc != nil && toAcc.BalanceZNHB != nil && toAcc.BalanceZNHB.Cmp(routed) >= 0 {
				toAcc.BalanceZNHB.Sub(toAcc.BalanceZNHB, routed)
				deducted = true
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:382`

````
if !deducted && fromAcc != nil && fromAcc.BalanceZNHB != nil && fromAcc.BalanceZNHB.Cmp(routed) >= 0 {
				fromAcc.BalanceZNHB.Sub(fromAcc.BalanceZNHB, routed)
				deducted = true
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:386`

````
default:
			return fmt.Errorf("fees: unsupported asset %s", result.Asset)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:389`

````
if !deducted {
			return fmt.Errorf("fees: insufficient balance to route fee")
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:396`

````
switch result.Asset {
		case fees.AssetNHB:
			if routeAcc.BalanceNHB == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:401`

````
routeAcc.BalanceNHB.Add(routeAcc.BalanceNHB, routed)
		case fees.AssetZNHB:
			if routeAcc.BalanceZNHB == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:402`

````
case fees.AssetZNHB:
			if routeAcc.BalanceZNHB == nil {
				routeAcc.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:403`

````
if routeAcc.BalanceZNHB == nil {
				routeAcc.BalanceZNHB = big.NewInt(0)
			}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:405`

````
}
			routeAcc.BalanceZNHB.Add(routeAcc.BalanceZNHB, routed)
		default:
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:407`

````
default:
			return fmt.Errorf("fees: unsupported asset %s", result.Asset)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:413`

````
}
	sp.AppendEvent(events.FeeApplied{
		Payer:             payer,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:415`

````
Payer:             payer,
		Domain:            fees.NormalizeDomain(domain),
		Asset:             result.Asset,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:418`

````
Gross:             new(big.Int).Set(gross),
		Fee:               cloneBigInt(result.Fee),
		Net:               cloneBigInt(result.Net),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:427`

````
WindowStart:       result.WindowStart,
		FeeBasisPoints:    result.FeeBasisPoints,
	}.Event())
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:441`

````
func feeWindowStart(ts time.Time) time.Time {
	if ts.IsZero() {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:449`

````
func sameFeeWindow(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:564`

````
// SetEscrowFeeTreasury configures the address receiving escrow fees during
// release transitions.
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:566`

````
// release transitions.
func (sp *StateProcessor) SetEscrowFeeTreasury(addr [20]byte) {
	sp.escrowFeeTreasury = addr
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:567`

````
func (sp *StateProcessor) SetEscrowFeeTreasury(addr [20]byte) {
	sp.escrowFeeTreasury = addr
}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:581`

````
manager := nhbstate.NewManager(sp.Trie)
		if _, err := manager.FeesEnsureMonthlyRollover(timestamp); err != nil {
			log.Printf("fees: monthly rollover check failed: %v", err)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:582`

````
if _, err := manager.FeesEnsureMonthlyRollover(timestamp); err != nil {
			log.Printf("fees: monthly rollover check failed: %v", err)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:621`

````
manager := nhbstate.NewManager(sp.Trie)
	if _, err := manager.AddProposedTodayZNHB(now, demand); err != nil {
		sp.blockCtx.PendingRewards.ClearPendingRewards()
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:626`

````
budget, err := manager.GetRemainingDailyBudgetZNHB(now)
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:660`

````
}
	if treasuryAcc.BalanceZNHB == nil {
		treasuryAcc.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:661`

````
if treasuryAcc.BalanceZNHB == nil {
		treasuryAcc.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:671`

````
for _, reward := range pending {
		if reward.AmountZNHB == nil || reward.AmountZNHB.Sign() <= 0 {
			continue
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:677`

````
}
		amount := new(big.Int).Set(reward.AmountZNHB)
		payout := new(big.Int).Mul(amount, ratioNum)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:689`

````
}
		if treasuryAcc.BalanceZNHB.Cmp(payout) < 0 {
			payout = new(big.Int).Set(treasuryAcc.BalanceZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:690`

````
if treasuryAcc.BalanceZNHB.Cmp(payout) < 0 {
			payout = new(big.Int).Set(treasuryAcc.BalanceZNHB)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:704`

````
}
			if acct.BalanceZNHB == nil {
				acct.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:705`

````
if acct.BalanceZNHB == nil {
				acct.BalanceZNHB = big.NewInt(0)
			}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:710`

````
}
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, payout)
		treasuryAcc.BalanceZNHB = new(big.Int).Sub(treasuryAcc.BalanceZNHB, payout)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:711`

````
account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, payout)
		treasuryAcc.BalanceZNHB = new(big.Int).Sub(treasuryAcc.BalanceZNHB, payout)
		budgetRemaining.Sub(budgetRemaining, payout)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:731`

````
}
		paidTodayTotal, err = manager.AddPaidTodayZNHB(now, paidTotal)
		if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:737`

````
} else {
		paidTodayTotal, _ = manager.AddPaidTodayZNHB(now, big.NewInt(0))
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:759`

````
Day:        now.UTC().Format("2006-01-02"),
			BudgetZNHB: budget,
			DemandZNHB: demand,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:760`

````
BudgetZNHB: budget,
			DemandZNHB: demand,
			RatioFP:    ratioFP,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1086`

````
}
	if treasuryAcc.BalanceZNHB == nil {
		treasuryAcc.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1087`

````
if treasuryAcc.BalanceZNHB == nil {
		treasuryAcc.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1089`

````
}
	treasuryBalance := new(big.Int).Set(treasuryAcc.BalanceZNHB)
	budget := new(big.Int).Set(emission)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1143`

````
if totalPaid.Sign() > 0 {
			treasuryAcc.BalanceZNHB = new(big.Int).Sub(treasuryAcc.BalanceZNHB, totalPaid)
			if err := manager.PutAccount(cfg.TreasuryAddress[:], treasuryAcc); err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1154`

````
}
				if account.BalanceZNHB == nil {
					account.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1155`

````
if account.BalanceZNHB == nil {
					account.BalanceZNHB = big.NewInt(0)
				}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1157`

````
}
				account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, winner.Amount)
				if err := manager.PutAccount(winner.Address[:], account); err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1364`

````
intentTTL:             sp.intentTTL,
		feePolicy:             sp.feePolicy.Clone(),
		blockCtx:              sp.blockCtx,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1437`

````
result, err = sp.applyEvmTransaction(tx)
	case types.TxTypeTransferZNHB:
		result, err = sp.applyTransferZNHB(tx, sender, senderAccount)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1438`

````
case types.TxTypeTransferZNHB:
		result, err = sp.applyTransferZNHB(tx, sender, senderAccount)
	default:
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1444`

````
if err != nil {
		if len(sp.events) > start && !errors.Is(err, ErrTransferZNHBPaused) && !errors.Is(err, ErrTransferNHBPaused) {
			sp.events = sp.events[:start]
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1591`

````
Difficulty:  big.NewInt(0),
		BaseFee:     big.NewInt(0),
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1611`

````
GasPrice:      tx.GasPrice,
		GasFeeCap:     tx.GasPrice,
		GasTipCap:     tx.GasPrice,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1615`

````
AccessList:    nil,
		BlobGasFeeCap: nil,
		BlobHashes:    nil,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1620`

````
evm := gethvm.NewEVM(blockCtx, statedb, params.TestChainConfig, gethvm.Config{NoBaseFee: true})
	evm.SetTxContext(txCtx)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1758`

````
if err := sp.applyTransactionFee(tx, from, fromAcc, toAcc); err != nil {
		return nil, err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1811`

````
func (sp *StateProcessor) applyTransferZNHB(tx *types.Transaction, sender []byte, senderAccount *types.Account) (*SimulationResult, error) {
	if sp == nil || tx == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1813`

````
if sp == nil || tx == nil {
		return nil, fmt.Errorf("znhb transfer: state unavailable")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1815`

````
}
	if sp.pauses != nil && sp.pauses.IsPaused(moduleTransferZNHB) {
		sp.emitTransferZNHBBlocked(tx, sender, "paused by governance")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1816`

````
if sp.pauses != nil && sp.pauses.IsPaused(moduleTransferZNHB) {
		sp.emitTransferZNHBBlocked(tx, sender, "paused by governance")
		return nil, ErrTransferZNHBPaused
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1817`

````
sp.emitTransferZNHBBlocked(tx, sender, "paused by governance")
		return nil, ErrTransferZNHBPaused
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1820`

````
if len(tx.To) != common.AddressLength {
		return nil, fmt.Errorf("znhb transfer: recipient address required")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1824`

````
if bytes.Equal(tx.To, zeroAddress[:]) {
		return nil, fmt.Errorf("znhb transfer: recipient address invalid")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1827`

````
if tx.Value == nil || tx.Value.Sign() <= 0 {
		return nil, fmt.Errorf("znhb transfer: amount must be positive")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1830`

````
if senderAccount == nil {
		return nil, fmt.Errorf("znhb transfer: sender account unavailable")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1833`

````
amount := new(big.Int).Set(tx.Value)
	if senderAccount.BalanceZNHB == nil {
		senderAccount.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1834`

````
if senderAccount.BalanceZNHB == nil {
		senderAccount.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1836`

````
}
	if senderAccount.BalanceZNHB.Cmp(amount) < 0 {
		return nil, fmt.Errorf("znhb transfer: insufficient balance")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1837`

````
if senderAccount.BalanceZNHB.Cmp(amount) < 0 {
		return nil, fmt.Errorf("znhb transfer: insufficient balance")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1839`

````
}
	senderAccount.BalanceZNHB = new(big.Int).Sub(senderAccount.BalanceZNHB, amount)
	selfTransfer := bytes.Equal(sender, tx.To)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1848`

````
}
		if recipientAccount.BalanceZNHB == nil {
			recipientAccount.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1849`

````
if recipientAccount.BalanceZNHB == nil {
			recipientAccount.BalanceZNHB = big.NewInt(0)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1852`

````
}
	recipientAccount.BalanceZNHB = new(big.Int).Add(recipientAccount.BalanceZNHB, amount)
	if err := sp.applyTransactionFee(tx, sender, senderAccount, recipientAccount); err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1853`

````
recipientAccount.BalanceZNHB = new(big.Int).Add(recipientAccount.BalanceZNHB, amount)
	if err := sp.applyTransactionFee(tx, sender, senderAccount, recipientAccount); err != nil {
		return nil, err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1874`

````
if err != nil {
		return nil, fmt.Errorf("znhb transfer: compute hash: %w", err)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1877`

````
if len(hashBytes) != len(txHash) {
		return nil, fmt.Errorf("znhb transfer: expected 32-byte tx hash, got %d", len(hashBytes))
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1885`

````
evt := events.Transfer{
		Asset:  "ZNHB",
		From:   senderAddr,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1895`

````
if metrics := observability.Events(); metrics != nil {
		metrics.RecordTransfer("ZNHB")
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1922`

````
func (sp *StateProcessor) emitTransferZNHBBlocked(tx *types.Transaction, sender []byte, reason string) {
	if sp == nil || tx == nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1926`

````
}
	evt := events.TransferZNHBBlocked{
		Asset:  "ZNHB",
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1927`

````
evt := events.TransferZNHBBlocked{
		Asset:  "ZNHB",
		Reason: strings.TrimSpace(reason),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1984`

````
requiredRole = "MINTER_NHB"
	case "ZNHB":
		requiredRole = "MINTER_ZNHB"
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:1985`

````
case "ZNHB":
		requiredRole = "MINTER_ZNHB"
	default:
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2029`

````
account.BalanceNHB = new(big.Int).Add(account.BalanceNHB, amount)
	case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2030`

````
case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2194`

````
Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2234`

````
copy(meta[:], payload.Meta)
	if _, err := sp.EscrowEngine.Create(payer, payee, payload.Token, payload.Amount, payload.FeeBps, payload.Deadline, payload.Nonce, &mediatorAddr, meta, strings.TrimSpace(payload.Realm)); err != nil {
		return err
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2463`

````
}
	if delegatorAcc.BalanceZNHB.Cmp(amount) < 0 {
		return nil, fmt.Errorf("insufficient ZapNHB")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2466`

````
}
	if len(delegatorAcc.DelegatedValidator) > 0 && !bytes.Equal(delegatorAcc.DelegatedValidator, target) && delegatorAcc.LockedZNHB.Sign() > 0 {
		return nil, fmt.Errorf("existing delegation must be fully undelegated before switching validators")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2479`

````
delegatorAcc.BalanceZNHB.Sub(delegatorAcc.BalanceZNHB, amount)
	delegatorAcc.LockedZNHB.Add(delegatorAcc.LockedZNHB, amount)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2480`

````
delegatorAcc.BalanceZNHB.Sub(delegatorAcc.BalanceZNHB, amount)
	delegatorAcc.LockedZNHB.Add(delegatorAcc.LockedZNHB, amount)
	delegatorAcc.DelegatedValidator = append([]byte(nil), target...)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2526`

````
Amount:      new(big.Int).Set(amount),
		Locked:      new(big.Int).Set(delegatorAcc.LockedZNHB),
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2557`

````
}
	if delegatorAcc.LockedZNHB.Cmp(amount) < 0 {
		return nil, fmt.Errorf("insufficient locked stake")
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2580`

````
}
	delegatorAcc.LockedZNHB.Sub(delegatorAcc.LockedZNHB, amount)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2595`

````
delegatorAcc.NextUnbondingID = nextID + 1
	if delegatorAcc.LockedZNHB.Sign() == 0 {
		delegatorAcc.DelegatedValidator = nil
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2699`

````
delegatorAcc.PendingUnbonds = append(delegatorAcc.PendingUnbonds[:index], delegatorAcc.PendingUnbonds[index+1:]...)
	delegatorAcc.BalanceZNHB.Add(delegatorAcc.BalanceZNHB, entry.Amount)
	delegatorAcc.Nonce++
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2835`

````
if minted.Sign() > 0 {
		account.BalanceZNHB.Add(account.BalanceZNHB, minted)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2856`

````
capEvt := events.StakeCapHit{
			RequestedZNHB: attemptedMint,
			AllowedZNHB:   new(big.Int).Set(minted),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2857`

````
RequestedZNHB: attemptedMint,
			AllowedZNHB:   new(big.Int).Set(minted),
			YTD:           new(big.Int).Set(updatedEmission),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:2869`

````
Addr:             claimedAddr,
		PaidZNHB:         new(big.Int).Set(minted),
		Periods:          periods,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3102`

````
type accountMetadata struct {
	BalanceZNHB             *big.Int
	Stake                   *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3107`

````
StakeLastPayoutTs       uint64
	LockedZNHB              *big.Int
	CollateralBalance       *big.Int
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3133`

````
}
	if account.BalanceZNHB == nil {
		account.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3134`

````
if account.BalanceZNHB == nil {
		account.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3145`

````
}
	if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3146`

````
if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3222`

````
BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(0),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3245`

````
if meta != nil {
		if meta.BalanceZNHB != nil {
			account.BalanceZNHB = new(big.Int).Set(meta.BalanceZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3246`

````
if meta.BalanceZNHB != nil {
			account.BalanceZNHB = new(big.Int).Set(meta.BalanceZNHB)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3257`

````
}
		if meta.LockedZNHB != nil {
			account.LockedZNHB = new(big.Int).Set(meta.LockedZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3258`

````
if meta.LockedZNHB != nil {
			account.LockedZNHB = new(big.Int).Set(meta.LockedZNHB)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3371`

````
meta := &accountMetadata{
		BalanceZNHB:               new(big.Int).Set(account.BalanceZNHB),
		Stake:                     new(big.Int).Set(account.Stake),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3376`

````
StakeLastPayoutTs:         account.StakeLastPayoutTs,
		LockedZNHB:                new(big.Int).Set(account.LockedZNHB),
		CollateralBalance:         new(big.Int).Set(account.CollateralBalance),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3861`

````
meta := &accountMetadata{
		BalanceZNHB:        new(big.Int).Set(legacy.BalanceZNHB),
		Stake:              new(big.Int).Set(legacy.Stake),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3866`

````
StakeLastPayoutTs:  legacy.StakeLastPayoutTs,
		LockedZNHB:         big.NewInt(0),
		CollateralBalance:  big.NewInt(0),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3923`

````
meta := &accountMetadata{
		BalanceZNHB:    big.NewInt(0),
		Stake:          big.NewInt(0),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3927`

````
StakeLastIndex: big.NewInt(0),
		LockedZNHB:     big.NewInt(0),
		Unbonding:      make([]stakeUnbond, 0),
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3936`

````
}
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3937`

````
if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3948`

````
}
	if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3949`

````
if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3979`

````
func (sp *StateProcessor) writeAccountMetadata(addr []byte, meta *accountMetadata) error {
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3980`

````
if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3991`

````
}
	if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:3992`

````
if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4273`

````
Amount:    amount,
		FeeBps:    0,
		Deadline:  deadline,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4301`

````
account.BalanceNHB = new(big.Int).Add(account.BalanceNHB, amount)
	case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4302`

````
case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4446`

````
pending := nhbstate.PendingReward{
		AmountZNHB: new(big.Int).Set(reward),
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4464`

````
if evt := (events.LoyaltyRewardProposed{TxHash: pending.TxHash, Amount: pending.AmountZNHB}).Event(); evt != nil {
		sp.AppendEvent(evt)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4474`

````
now := sp.blockTimestamp()
	totalProposed, err := manager.AddProposedTodayZNHB(now, amount)
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4483`

````
}
	if treasuryAcc.BalanceZNHB == nil {
		treasuryAcc.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4484`

````
if treasuryAcc.BalanceZNHB == nil {
		treasuryAcc.BalanceZNHB = big.NewInt(0)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4488`

````
payout := new(big.Int).Set(amount)
	if treasuryAcc.BalanceZNHB.Cmp(payout) < 0 {
		payout = new(big.Int).Set(treasuryAcc.BalanceZNHB)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4489`

````
if treasuryAcc.BalanceZNHB.Cmp(payout) < 0 {
		payout = new(big.Int).Set(treasuryAcc.BalanceZNHB)
	}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4494`

````
if payout.Sign() > 0 {
		treasuryAcc.BalanceZNHB = new(big.Int).Sub(treasuryAcc.BalanceZNHB, payout)
		if err := sp.setAccount(cfg.Treasury, treasuryAcc); err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4513`

````
}
		if account.BalanceZNHB == nil {
			account.BalanceZNHB = big.NewInt(0)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4514`

````
if account.BalanceZNHB == nil {
			account.BalanceZNHB = big.NewInt(0)
		}
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4516`

````
}
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, payout)
		if persist {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4523`

````
paidTotal, err = manager.AddPaidTodayZNHB(now, payout)
		if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4528`

````
} else {
		paidTotal, err = manager.AddPaidTodayZNHB(now, nil)
		if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4534`

````
budget, err := manager.GetRemainingDailyBudgetZNHB(now)
	if err != nil {
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4562`

````
Day:        now.UTC().Format("2006-01-02"),
			BudgetZNHB: budget,
			DemandZNHB: requested,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4563`

````
BudgetZNHB: budget,
			DemandZNHB: requested,
			RatioFP:    ratioFP,
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4579`

````
sp.EscrowEngine.SetEmitter(stateProcessorEmitter{sp: sp})
	sp.EscrowEngine.SetFeeTreasury(sp.escrowFeeTreasury)
	sp.EscrowEngine.SetNowFunc(func() int64 { return sp.now().Unix() })
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4844`

````
return sp.setAccount(addr, account)
	case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

### Fee deduction missing balance guard (WARN)

Fee deduction should assert sender balance >= fee prior to mutation.

**Proofs:**

- `core/state_transition.go:4845`

````
case "ZNHB":
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, amount)
		return sp.setAccount(addr, account)
````

**Remediation:**

- Introduce guard and fail the transaction if the account cannot pay the fee.

## Fee / Free-tier Rollover

Monthly fee windows require rollover/reset to prevent unbounded accumulation.

### Missing free-tier rollover logic (WARN)

Fee counters lack explicit rollover/reset handling.

**Proofs:**

- `clients/ts/fees/v1/events.ts:1`

````
// Code generated by protoc-gen-ts_proto. DO NOT EDIT.
````

**Remediation:**

- Introduce monthly window keys and reset tasks for fee counters.

### Missing free-tier rollover logic (WARN)

Fee counters lack explicit rollover/reset handling.

**Proofs:**

- `core/events/fees.go:1`

````
package events
````

**Remediation:**

- Introduce monthly window keys and reset tasks for fee counters.

### Missing free-tier rollover logic (WARN)

Fee counters lack explicit rollover/reset handling.

**Proofs:**

- `docs/fees/routing.md:1`

````
# Fee routing
````

**Remediation:**

- Introduce monthly window keys and reset tasks for fee counters.

### Missing free-tier rollover logic (WARN)

Fee counters lack explicit rollover/reset handling.

**Proofs:**

- `native/fees/apply.go:1`

````
package fees
````

**Remediation:**

- Introduce monthly window keys and reset tasks for fee counters.

### Missing free-tier rollover logic (WARN)

Fee counters lack explicit rollover/reset handling.

**Proofs:**

- `native/fees/codec.go:1`

````
package fees
````

**Remediation:**

- Introduce monthly window keys and reset tasks for fee counters.

### Missing free-tier rollover logic (WARN)

Fee counters lack explicit rollover/reset handling.

**Proofs:**

- `proto/fees/v1/events.proto:1`

````
syntax = "proto3";
````

**Remediation:**

- Introduce monthly window keys and reset tasks for fee counters.

## Pauses & Governance

Governance pause switches should gate transfers, staking, and swaps across RPC layers.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `clients/ts/swap/v1/stable.ts:1`

````
// Code generated by protoc-gen-ts_proto. DO NOT EDIT.
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `clients/ts/swap/v1/swap.ts:1`

````
// Code generated by protoc-gen-ts_proto. DO NOT EDIT.
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `clients/ts/swap/v1/tx.ts:1`

````
// Code generated by protoc-gen-ts_proto. DO NOT EDIT.
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `cmd/gateway/main.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `cmd/nhb-cli/swap.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `cmd/swap-audit/main.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `core/events/swap.go:1`

````
package events
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `core/state/staking_keys.go:1`

````
package state
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/compose/config/gateway.yaml:1`

````
listen: ":8080"
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/compose/config/swapd.yaml:1`

````
listen: ":7074"
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/gateway/Chart.yaml:1`

````
apiVersion: v2
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/gateway/templates/_helpers.tpl:1`

````
{{- define "gateway.name" -}}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/gateway/templates/configmap.yaml:1`

````
{{- if .Values.config.enabled }}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/gateway/templates/deployment.yaml:1`

````
apiVersion: apps/v1
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/gateway/templates/ingress.yaml:1`

````
{{- if .Values.ingress.enabled }}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/gateway/templates/service.yaml:1`

````
apiVersion: v1
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/gateway/values.yaml:1`

````
replicaCount: 2
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/swapd/Chart.yaml:1`

````
apiVersion: v2
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/swapd/templates/_helpers.tpl:1`

````
{{- define "swapd.name" -}}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/swapd/templates/configmap.yaml:1`

````
{{- if .Values.config.enabled }}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/swapd/templates/service.yaml:1`

````
apiVersion: v1
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/swapd/templates/statefulset.yaml:1`

````
apiVersion: apps/v1
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/swapd/values.yaml:1`

````
replicaCount: 1
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/values/dev/gateway.yaml:1`

````
image:
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/values/dev/swapd.yaml:1`

````
image:
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/values/prod/gateway.yaml:1`

````
image:
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/values/prod/swapd.yaml:1`

````
image:
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/values/staging/gateway.yaml:1`

````
image:
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `deploy/helm/values/staging/swapd.yaml:1`

````
image:
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/api/gateway-pos.md:1`

````
# POS gateway HTTP API
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/cli/staking.md:1`

````
# Staking CLI
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/escrow/gateway-api.md:1`

````
# Escrow Gateway REST API
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/escrow/nhbchain-escrow-gateway.md:1`

````
# NHBCHAIN EPIC — Escrow Gateway (REST) + Disputes/Arbitration + P2P Market Hooks
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/gateway/overview.md:1`

````
# Gateway Overview
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/gateway/transactions.md:1`

````
# Transaction submission via the gateway
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/identity/identity-gateway.md:1`

````
# Identity Gateway REST API
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/migrate/monolith-to-gateway.md:1`

````
# Migrating from the Legacy JSON-RPC Node
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/swap/admin.md:1`

````
# Swap Mint Admin Runbook
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/swap/oracle-verification.md:1`

````
# Swap Oracle Price Proofs
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/swap/oracle.md:1`

````
# Swap Oracle Operations
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/swap/oracles.md:1`

````
# Oracle Aggregation
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/swap/overview.md:1`

````
# Swap Voucher RPC Summary
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/swap/regulatory-brief.md:1`

````
# SWAP-4 Regulatory Brief
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/swap/stable-ledger.md:1`

````
# Stable Ledger Records
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/swap/state-indexes.md:1`

````
# Swap state indexes
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/swap/treasury.md:1`

````
# Treasury Controls for Swap Flows
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `docs/transactions/znhb-transfer.md:1`

````
# NHB vs. ZNHB transfers
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `examples/gateway/openapi.yaml:1`

````
openapi: 3.0.3
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `examples/swap/oracle-publisher.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `examples/swap/redeem.ts:1`

````
import fetch from "node-fetch";
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `examples/wallet-lite/app/lib/identity-gateway.ts:1`

````
import crypto from 'crypto';
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/auth/auth.go:1`

````
package auth
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/compat/compat.go:1`

````
package compat
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/compat/deprecation.go:1`

````
package compat
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/compat/deprecations.yaml:1`

````
series: Monolithic JSON-RPC compatibility decommission
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/compat/mapping.go:1`

````
package compat
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/config.yaml:1`

````
listen: ":8080"
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/config/config.go:1`

````
package config
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/middleware/auth.go:1`

````
package middleware
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/middleware/cors.go:1`

````
package middleware
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/middleware/observability.go:1`

````
package middleware
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/middleware/ratelimit.go:1`

````
package middleware
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/routes/lending.go:1`

````
package routes
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/routes/proxy.go:1`

````
package routes
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/routes/router.go:1`

````
package routes
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `gateway/routes/transactions.go:1`

````
package routes
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/bank/transfer.go:1`

````
package bank
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/engine.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/keys.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/ledger.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/oracle.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/oracle_verify.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/params.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/redeem.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/sanctions.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/stable_store.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/types.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `native/swap/voucher.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `ops/audit-pack/config-samples/rpc-gateway.yaml:1`

````
service:
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `proto/swap/v1/stable.pb.go:1`

````
// Code generated by protoc-gen-go. DO NOT EDIT.
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `proto/swap/v1/stable.proto:1`

````
syntax = "proto3";
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `proto/swap/v1/swap.pb.go:1`

````
// Code generated by protoc-gen-go. DO NOT EDIT.
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `proto/swap/v1/swap.proto:1`

````
syntax = "proto3";
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `proto/swap/v1/swap_grpc.pb.go:1`

````
// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `proto/swap/v1/tx.pb.go:1`

````
// Code generated by protoc-gen-go. DO NOT EDIT.
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `proto/swap/v1/tx.proto:1`

````
syntax = "proto3";
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `rpc/swap_admin_handlers.go:1`

````
package rpc
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `rpc/swap_stable_handlers.go:1`

````
package rpc
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `sdk/go/identity/gateway/client.go:1`

````
package gateway
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `sdk/swap/client.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `sdk/swap/tx.go:1`

````
package swap
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `sdk/ts/src/identityGateway.ts:1`

````
import crypto from 'crypto';
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `sdk/ts/test/identityGateway.test.ts:1`

````
import { createServer } from 'http';
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/auth.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/config.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/main.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/node_client.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/payintent.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/server.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/storage.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/watcher.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/webhook.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/escrow-gateway/webhook_queue.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/identity-gateway/cmd/identity-gateway/main.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/identity-gateway/emailer.go:1`

````
package identitygateway
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/identity-gateway/server.go:1`

````
package identitygateway
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/identity-gateway/store.go:1`

````
package identitygateway
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/auth/auth.go:1`

````
package auth
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/config/config.go:1`

````
package config
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/funding/processor.go:1`

````
package funding
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/hsm/client.go:1`

````
package hsm
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/identity/client.go:1`

````
package identity
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/main.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/middleware/idempotency.go:1`

````
package middleware
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/models/models.go:1`

````
package models
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/recon/reconciler.go:1`

````
package recon
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/recon/scheduler.go:1`

````
package recon
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/server/funding.go:1`

````
package server
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/server/partners.go:1`

````
package server
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/server/server.go:1`

````
package server
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/server/sign_submit.go:1`

````
package server
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/server/workflow.go:1`

````
package server
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/otc-gateway/swaprpc/client.go:1`

````
package swaprpc
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/payments-gateway/config.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/payments-gateway/kms.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/payments-gateway/main.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/payments-gateway/node_client.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/payments-gateway/nowpayments.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/payments-gateway/oracle.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/payments-gateway/server.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/payments-gateway/storage.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swap-gateway/client_node.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swap-gateway/main.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swap-gateway/nowpayments.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swap-gateway/quote.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swap-gateway/voucher.go:1`

````
package main
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/adapters/sources.go:1`

````
package adapters
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/oracle/manager.go:1`

````
package oracle
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/server/auth.go:1`

````
package server
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/server/server.go:1`

````
package server
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/server/stable_handlers.go:1`

````
package server
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/server/testdata/stable_cashout.json:1`

````
{"intent_id":"i-1717787726000000000","reservation_id":"q-1717787719000000000","amount":102,"created_at":"2024-06-07T19:15:26Z","trace_id":"102030405060708090a0b0c0d0e0f001"}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/server/testdata/stable_disabled.json:1`

````
{"error":"stable engine not enabled"}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/server/testdata/stable_limits.json:1`

````
{"daily_cap":1000000,"asset_caps":{"ZNHB":{"max_slippage_bps":50,"quote_ttl_seconds":60,"soft_inventory":1000000}}}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/server/testdata/stable_quote.json:1`

````
{"quote_id":"q-1717787719000000000","asset":"ZNHB","price":1.02,"expires_at":"2024-06-07T19:16:19Z","trace_id":"102030405060708090a0b0c0d0e0f001"}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/server/testdata/stable_reserve.json:1`

````
{"reservation_id":"q-1717787719000000000","quote_id":"q-1717787719000000000","amount_in":100,"amount_out":102,"expires_at":"2024-06-07T19:16:19Z","trace_id":"102030405060708090a0b0c0d0e0f001"}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/server/testdata/stable_status.json:1`

````
{"quotes":1,"reservations":0,"assets":1,"updated_at":"2024-06-07T19:15:27Z"}
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/stable/cashout.go:1`

````
package stable
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/stable/engine.go:1`

````
package stable
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/stable/quotes.go:1`

````
package stable
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/stable/reserve.go:1`

````
package stable
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

### Pause flag not referenced (WARN)

Critical handlers should consult global pause/governance flags.

**Proofs:**

- `services/swapd/storage/storage.go:1`

````
package storage
````

**Remediation:**

- Wire pause toggles through handler and enforce gating before state mutations.

## DoS & QoS

Gateways and mempool must enforce rate limits and queue caps to mitigate DoS.

### Missing rate limiting (WARN)

Handlers should enforce per-IP or per-key limits to avoid DoS.

**Proofs:**

- `services/escrow-gateway/server.go:1`

````
package main
````

**Remediation:**

- Add middleware or queue caps to bound inbound load and document SLOs.

### Missing rate limiting (WARN)

Handlers should enforce per-IP or per-key limits to avoid DoS.

**Proofs:**

- `services/governd/config/server.crt:1`

````
-----BEGIN CERTIFICATE-----
````

**Remediation:**

- Add middleware or queue caps to bound inbound load and document SLOs.

### Missing rate limiting (WARN)

Handlers should enforce per-IP or per-key limits to avoid DoS.

**Proofs:**

- Private TLS key material previously stored in `services/governd/config/server.key`
  has been purged from the repository. Runtime deployments must now supply
  secrets via environment variables (`signer_key_env`, `tls.key_env`). See
  [Security – Key management and rotation](../../SECURITY.md#key-management-and-rotation)
  for the updated procedure.

**Remediation:**

- Completed – key material rotated off-repo with automated scanning to prevent
  regressions.

### Missing rate limiting (WARN)

Handlers should enforce per-IP or per-key limits to avoid DoS.

**Proofs:**

- `services/governd/server/server.go:1`

````
package server
````

**Remediation:**

- Add middleware or queue caps to bound inbound load and document SLOs.

### Missing rate limiting (WARN)

Handlers should enforce per-IP or per-key limits to avoid DoS.

**Proofs:**

- `services/lending/server/server.go:1`

````
package server
````

**Remediation:**

- Add middleware or queue caps to bound inbound load and document SLOs.

### Missing rate limiting (WARN)

Handlers should enforce per-IP or per-key limits to avoid DoS.

**Proofs:**

- `services/otc-gateway/server/server.go:1`

````
package server
````

**Remediation:**

- Add middleware or queue caps to bound inbound load and document SLOs.

### Missing rate limiting (WARN)

Handlers should enforce per-IP or per-key limits to avoid DoS.

**Proofs:**

- `services/payments-gateway/server.go:1`

````
package main
````

**Remediation:**

- Add middleware or queue caps to bound inbound load and document SLOs.

### Missing rate limiting (WARN)

Handlers should enforce per-IP or per-key limits to avoid DoS.

**Proofs:**

- `services/swapd/server/server.go:1`

````
package server
````

**Remediation:**

- Add middleware or queue caps to bound inbound load and document SLOs.

### Missing rate limiting (WARN)

Handlers should enforce per-IP or per-key limits to avoid DoS.

**Proofs:**

- `services/swapd/server/stable_handlers.go:1`

````
package server
````

**Remediation:**

- Add middleware or queue caps to bound inbound load and document SLOs.

## File Serving & Path Traversal

File handlers need strict path validation to avoid traversal.

No issues detected.

## External Dependencies

Track upstream CVEs; upgrade or mitigate when govulncheck reports issues.

No issues detected.

