# Security Program Overview

We operate a coordinated vulnerability disclosure program to keep NHBChain participants safe.

- **Bug bounty:** Learn about scope, reward tiers, and response targets in [`docs/security/bug-bounty.md`](docs/security/bug-bounty.md).
- **Responsible disclosure:** Reporting instructions, SLAs, and embargo expectations are documented in [`docs/security/disclosure.md`](docs/security/disclosure.md).
- **Audit readiness:** External assessors can access frozen commits, build steps, and fixtures via [`docs/security/audit-readiness.md`](docs/security/audit-readiness.md).
- **PGP key:** Our repository key and fingerprint live in [`docs/security/repository-pgp-key.asc`](docs/security/repository-pgp-key.asc).

To report a vulnerability, encrypt your findings with the repository PGP key and email `security@nehborly.net`. For urgent issues, escalate via Signal at `+13234559568`.

## Key management and rotation

Private keys used by services and SDK examples are no longer stored in the
repository. Operators must inject signing and TLS material at runtime using the
new environment variable hooks (`signer_key_env`, `tls.key_env`, etc.). When
rotating credentials:

1. Provision the replacement key in the chosen secret manager (Kubernetes
   secrets, Vault, AWS Secrets Manager, and so on) and update the environment
   variable or file path exposed to the process.
2. Restart the affected deployments so they pick up the new secret material.
3. Revoke or shred the superseded key material to prevent reuse.

CI now enforces these expectations with a `git secrets` scan that fails the
pipeline if PEM private key headers are committed. Follow the steps above if the
scan highlights historical files and notify security for retrospective key
rotation.

