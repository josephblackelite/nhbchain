# POS-OPS-10: Merchant onboarding & device attestation runbooks

## Summary

* Added a merchant and device onboarding runbook (`docs/runbooks/pos-onboarding.md`) covering the POS registry records, who may sign a registry transaction, and how to read the records back. It does not cover KYC, sponsorship caps or key distribution.
* Added a device registry runbook (`docs/runbooks/device-attestation.md`). The code has no device attestation: there is no certificate issuance or revocation and no firmware hash, and certificate, HSM and firmware management are outside this repository's code.
* Captured paymaster budget management process with monitoring and alerting guidance.
* Established POS SLA and troubleshooting playbook for latency, QoS tuning, and incident response.

## Rollout

* Publish the runbooks to the operations knowledge base.
* Brief on-call engineers on the new procedures during the next readiness review.
