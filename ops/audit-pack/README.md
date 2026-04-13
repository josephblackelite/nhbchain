# NHBChain Audit Pack

This directory collects reproducible artifacts for external security audits. Artifacts are frozen to the commit listed in `FROZEN_COMMIT.txt`.

## Contents
- `FROZEN_COMMIT.txt` – canonical Git revision for this audit cycle.
- `BUILD_STEPS.md` – deterministic build and test process.
- `config-samples/` – sanitized validator, RPC, and wallet configuration files.
- `seeds-fixtures/` – deterministic seeds, genesis snapshots, and integration fixtures.

Auditors should review [`docs/security/audit-readiness.md`](../../docs/security/audit-readiness.md) for program expectations and contacts.

