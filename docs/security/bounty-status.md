# Bug Bounty Reports and Their Status

*Last updated: 2026-11-17.*

This page lists the researchers who have reported to us through the [bug bounty program](bug-bounty.md) and where their reports stand. It is updated each time we finish a round of triage. It names a researcher only when they have told us how they want to be credited; everyone else is listed without a name. Credits are on the [acknowledgments page](acknowledgments.md).

We do not publish what a report is about while any part of it is still being worked on, and we do not say which reports in a researcher's batch are confirmed and which are not. Reward amounts and disclosure timelines are handled privately with each researcher under our [disclosure policy](disclosure.md).

## Status key

- **Received**: we have acknowledged the report. It has not been assessed, or we are waiting for details we can read (see "How to send a report" below).
- **Under review**: we are checking the report against the code the validators run.
- **Investigated**: every report from this researcher has been assessed against the running code. Some are confirmed real and are being fixed in order of risk; others did not reproduce or were fixed before we read them. We do not publish which is which so as not to signal anything about an unresolved issue.
- **Resolved**: every report from this researcher is either fixed in the running network or does not apply to it.

## Researchers who asked to be credited by name

| Researcher | First received | Status |
| --- | --- | --- |
| sricharan pedhiti | 2026-10-07 | Resolved |
| Irman Fauzian (FZN Research) | 2026-10-18 | Resolved |
| BOB VERGA | 2026-10-22 | Resolved |
| oncodewa | 2026-11-03 | Resolved |
| Rd Ss | 2026-11-03 | Resolved |
| Rico Dimas | 2026-11-03 | Resolved |
| algero.reports | 2026-11-03 | Resolved |
| samirnof knof (0xlab) | 2026-10-23 | Investigated |
| Yusrizal Ahmad | 2026-10-31 | Investigated |
| aldiboncel49 | 2026-11-02 | Investigated (the five newest reports: Received, details to be sent as plain text) |
| Abu (@abubakarelzi) | 2026-11-02 | Investigated |
| ASHWINI JADHAO (Azethra) | 2026-11-02 | Investigated |
| Rudy Rusmanto | 2026-11-14 | Investigated |
| amigdala | 2026-10-26 | Under review |

## Researchers not named on this page

These researchers have not yet told us how to credit them, or asked to stay unnamed. We have asked each of them.

| Researchers | First received | Status |
| --- | --- | --- |
| Three researchers | 2026-10-05 | Received (encrypted reports or attached files; we have asked for the key facts as plain text) |
| Three researchers | 2026-11-10 | Investigated |
| Two researchers (21 reports) | 2026-11-15 | Under review |

## What to test

Please test against the code the validators run. As of 2026-11-17 the `main` branch carries it: its tree is the tree of the validator release `final-r10` (the `release/final-r10` branch, commit `8428e63`). Before this date `main` was behind the release, and several reports described issues that were already fixed in the running network. When a newer release is deployed, `main` is brought up to it and this page says so.

## How to send a report

- Email **security@nhbcoin.com** with the subject "Bug Bounty Submission". Encrypting to the [repository PGP key](repository-pgp-key.asc) is welcome (fingerprint `8C12 7674 689A AB92 A4DE  4643 E847 50CA 0E2F 4459`; check it matches what you import).
- Put the key facts in the message itself: the function or file, the steps, the effect, and the commit you tested. We do not open attachments, scripts or archives, so a report that is only an attachment waits until we have the facts as text.
- Keep the details private until we write to you, as described in the [disclosure policy](disclosure.md).
- Tell us how you want to be credited (by name, by handle, or not at all).

If you believe your entry above is wrong, or you want it changed, email **security@nhbcoin.com**.
