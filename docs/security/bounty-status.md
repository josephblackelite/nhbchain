# Bug Bounty Reports and Their Status

*Last updated: 2026-11-20.*

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
| aldiboncel49 | 2026-11-02 | Investigated (the five newest reports: Under review) |
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

Please test against the code the validators run. As of 2026-11-20 the `main` branch carries it: the code of the validator release `final-r16c` (commit `9c1c8d0`), which the validators have run since 2026-11-20 00:39 UTC. A node built from `main` runs the same code as the validators. The validators' binary (sha256 starting `c0dd21080213a3fe`) was built with Go 1.26.5, and a build for linux/amd64 with `CGO_ENABLED=0`, the flags in [`scripts/build.sh`](../../scripts/build.sh) and Go 1.26.5 gives that exact file. The join script builds with the toolchain `go.mod` names (Go 1.24.3), which gives different bytes from the same commit, so it checks the commit instead; a node built that way on a fresh server followed the validators' state with equal state roots on every height we compared. Before 2026-11-17 `main` was behind the release, and several reports described issues that were already fixed in the running network; between 2026-11-17 and 2026-11-18 it carried release `final-r10` (commit `8428e63`), and between 2026-11-18 and 2026-11-20 release `final-r16` (commit `44b77ce`).

The shipped `config.toml` now carries the activation heights the validators run. Earlier versions showed 0 for them, which means off and is not what the validators run, so a report that says a fix is disabled should be checked against the file in `main` first. The repository also still has older and unreleased branches (the `hard/*` branches and the older release branches); they are not the running code, so please test `main`.

Please name the commit you tested. When a newer release is deployed, `main` is brought up to it and this page says so.

## How to send a report

- Email **security@nhbcoin.com** with the subject "Bug Bounty Submission". Encrypting to the [repository PGP key](repository-pgp-key.asc) is welcome (fingerprint `8C12 7674 689A AB92 A4DE  4643 E847 50CA 0E2F 4459`; check it matches what you import).
- Put the key facts in the message itself: the function or file, the steps, the effect, and the commit you tested. We do not open attachments, scripts or archives, so a report that is only an attachment waits until we have the facts as text.
- Keep the details private until we write to you, as described in the [disclosure policy](disclosure.md).
- Tell us how you want to be credited (by name, by handle, or not at all).

If you believe your entry above is wrong, or you want it changed, email **security@nhbcoin.com**.
