# Supply Chain Security Guide

This document covers controls for managing dependencies, build systems, and release artifacts.

## Dependency management

- **Lockfiles.** Commit `go.sum`, `package-lock.json`, and equivalent files. Regenerate on updates to detect tampering.
- **Source pinning.** Use tagged releases or commit hashes for third-party modules. Avoid floating `latest` references.
- **Vulnerability scanning.** Run `go list -m -u all` and `npm audit` monthly. Track findings in the security backlog.

## Build pipeline

- **Reproducible builds.** Configure CI to build from clean containers with pinned base images. Compare build hashes between CI and local reproducible runs.
- **Code signing.** Sign binaries and container images using `cosign` with keys stored in HSM or KMS.
- **Access controls.** Restrict who can modify CI/CD pipelines. Require code review for pipeline changes.

## Artifact distribution

- Store release artifacts in immutable object storage with versioning enabled.
- Publish checksums and signatures alongside downloads.
- Maintain an SBOM (e.g., `syft packages dir`) for each release and archive it with the release notes.

## Incident handling

1. Freeze releases and notify stakeholders.
2. Identify the compromised dependency or build step.
3. Patch or replace affected components, regenerate artifacts, and update signatures.
4. Document the incident in `docs/security/disclosure.md` with remediation steps and follow-up actions.

## Continuous improvement

- Conduct quarterly dependency review meetings with engineering leads.
- Rotate signing keys annually or after any suspected compromise.
- Automate SBOM generation and verification in CI to catch drift early.
