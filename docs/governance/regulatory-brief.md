# Governance Regulatory Brief

> Include function-level documentation for developer integrations and technical specs; docs must be generated into /docs/governance/* for auditors, investors, regulators, and consumers.

## Purpose

- Provide regulators and oversight bodies with a concise summary of the
  governance system's mandate, safeguards, and accountability mechanisms.
- Clarify that governance powers are community-directed configuration changes,
  not instruments for profit sharing or investor returns.

## Stakeholder Responsibilities

- **Validators** operate consensus infrastructure, publish governance events,
  and enforce timelock delays. They do not hold discretionary control over
  treasury assets outside community-approved actions.
- **Community voters** review proposals, supply deposits to signal serious
  intent, and cast ballots according to the published eligibility snapshot.
  Participation is voluntary and does not entitle voters to revenue, dividends,
  or any expectation of profit derived from the efforts of others.
- **Foundation / Treasury operators** prepare policy drafts, ensure disclosures
  are attached to proposals, and execute approved actions once all procedural
  checkpoints clear. Operators are bound by the publicly auditable state machine
  and cannot bypass timelocks or snapshot requirements.

## Compliance Touchpoints

- **Deposit disclosures** clearly state that deposits are anti-spam escrows.
  They are returned automatically if the proposal passes or is withdrawn before
  entering voting, and may be slashed for malicious or abandoned submissions.
  Deposits are not investment products and provide no yield.
- **Voting transparency** is maintained through deterministic tally formulas and
  publicly accessible event logs. Proposal metadata, snapshot references, and
  final tallies are retained so independent parties can recreate results.
- **Timelock announcements** include the scheduled execution timestamp and the
  hash of the payload to be applied. This empowers watchdogs to review changes
  before they take effect and ensures affected integrators receive adequate
  notice.

## Reporting Cadence

- **Periodic summaries**: governance maintains a quarterly digest covering
  proposal statistics, parameter changes, and any timelock delays or vetoes.
- **Exceptional event notifications**: emergency actions, parameter rollbacks,
  or security-related governance items trigger same-day disclosures to
  validators, major integrators, and the public mailing list.
- **Regulatory liaisons**: upon request, the foundation provides detailed
  walkthroughs of specific proposals, including replayable state proofs and
  links to execution transactions, to assist auditors or regulators with case
  reviews.
