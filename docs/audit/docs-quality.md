# Documentation Quality Review Guide

Strong documentation ensures audit findings can be reproduced and remediations can be executed quickly. Use this checklist to evaluate the completeness and accuracy of technical content across the project.

## Scope

- Service READMEs (`services/*/README.md`)
- Operational runbooks (`docs/runbooks/`, `docs/ops/`)
- Architecture overviews (`docs/architecture/`, `docs/consensus/`)
- Onboarding guides (`docs/overview/`, `docs/sdk/`)

## Evaluation criteria

1. **Accuracy.** Verify instructions align with the current codebase and configuration defaults.
2. **Completeness.** Ensure critical workflows (deployment, upgrade, rollback, incident response) are covered end-to-end.
3. **Freshness.** Check commit history or embedded version tags to confirm the document has been updated within the last two releases.
4. **Traceability.** Confirm references link to actual code paths, dashboards, or runbooks.
5. **Accessibility.** Content should be concise, use consistent terminology, and provide prerequisite context.

## Review process

- Sample at least one document from each scope category.
- Walk through the steps as if you were a new operator; note missing prerequisites or ambiguous commands.
- Capture screenshots or logs when instructions are unclear or produce unexpected results.
- Create tickets for stale diagrams, broken links, or missing cross-references.

## Deliverables

- Annotated checklist summarizing which documents were reviewed and any action items.
- Pull requests or issues filed to address identified gaps.
- Suggested improvements to the documentation style guide (if recurring problems emerge).

## Exit criteria

- No critical documentation gaps remain unresolved.
- Runbooks and READMEs referenced during the audit include working commands and updated links.
- Audit artifact bundle includes the completed checklist and remediation tickets.
