# SRE Playbooks

Operational runbooks for NHB Chain key components. Each playbook includes detection steps, response procedures, and post-incident actions. Maintain copies in the runbook repo and keep PagerDuty services linked here.

## Key Management Service (KMS)

### Routine Key Rotation
- **Cadence**: Quarterly, or after any operator turnover.
- **Prerequisites**: Confirm quorum of authorized signers, ensure HSM firmware is current, and announce maintenance window.
- **Procedure**:
  1. Drain signing traffic by toggling the `kms_rotation_in_progress` flag via the admin RPC.
  2. Generate new key material inside the HSM; export public keys only.
  3. Update validator configs with new key IDs and distribute via secure channel.
  4. Run the `kms-rotation-smoke` script to sign test transactions on testnet.
  5. Re-enable signing traffic and monitor `kms_sign_success_rate` for 30 minutes.
- **Rollback**: Revert to previous key IDs stored in sealed backup, redeploy configs, and re-run smoke tests.
- **RACI**: SRE owns execution, security reviews approvals, validators acknowledge receipt.

### Incident: KMS Signing Failure
- **Detection**: Alert `kms_sign_error_rate > 1%` or repeated `kms_connection_timeout` logs.
- **Immediate Actions**:
  1. Page on-call SRE and security liaison.
  2. Switch validators to standby signing pool using the `kms failover` playbook.
  3. Capture HSM diagnostics and lock audit logs.
- **Recovery**: Restart KMS service, validate HSM health, run canary transaction. Rotate keys if compromise suspected.
- **Postmortem**: File incident report, update alert thresholds if needed, add regression tests.

## Oracle Operations

### Incident: Oracle Feed Stale
- **Detection**: Alert `nhb_oracle_update_age_seconds > 120` or dashboards showing flat price curve.
- **Immediate Actions**:
  1. Verify upstream data providers are reachable.
  2. Restart failed oracle workers and replay missed batches.
  3. Manually push latest price to testnet for validation.
- **Recovery**: Confirm on-chain submissions resume and freshness metric returns below 60s.
- **Escalation**: Notify data provider account managers if outage exceeds 15 minutes.

### Incident: Oracle Signature Skew
- **Detection**: Alert on signer imbalance > 40% skew.
- **Response**:
  1. Inspect signer health dashboard, identify lagging signer.
  2. Rotate credentials via secure KMS channel.
  3. Trigger signer reassignment script to rebalance load.
- **Post-Incident**: Update signer audit logs and review capacity planning.

## Validator Lifecycle

### Onboarding New Validator
- **Preparation**: Issue least-privilege API tokens, provide testnet access, and share onboarding checklist.
- **Steps**:
  1. Provision hardware per spec, install NHB Chain binaries, and sync from snapshot.
  2. Register validator keys with governance contract.
  3. Enable Prometheus scraping and log forwarding.
  4. Run `validator-onboarding-smoke` tests to confirm block signing and RPC connectivity.
  5. Submit onboarding report to network operations.

### Offboarding Validator
- **Trigger**: Voluntary exit, slash event, or compliance mandate.
- **Steps**:
  1. Disable consensus participation via admin RPC.
  2. Rotate out validator keys and revoke API tokens.
  3. Archive logs and metrics, store in cold storage for 90 days.
  4. Remove from Alertmanager routing and dashboard filters.
- **Verification**: Confirm no residual RPC or gossip traffic and that stake redistribution completed.

### Incident: Validator Outage
- **Detection**: Alert `nhb_validator_heartbeat_missing` or finality lag spike.
- **Immediate Response**:
  1. Page validator operations contact per RACI.
  2. Check host health (CPU, disk, network). Restart validator service if safe.
  3. Fallback to standby validator if downtime > 10 minutes.
- **Postmortem**: Review logs, update runbook, and schedule redundancy drill.

## Incident Drills

- Conduct quarterly game-days simulating KMS compromise, oracle outage, and multi-validator failure.
- Use chaos tooling to inject faults in staging prior to production exercises.
- Record lessons learned and feed back into runbooks and alert policies.
