# Latest bugcheck report

No report is checked in.

`scripts/publish_bugcheck.sh` copies the newest `audit/report-*.md` or `audit/bugcheck-*.md` (written by `scripts/bugcheck.sh`) over this file, copies it to `docs/audit/history/`, and updates [`index.md`](./index.md) and `_meta.json`. `scripts/english_audit.sh` appends an "English status report" section to this file after running `go test ./...` and the documentation snippet check. Run either script to generate a report for the commit you are reviewing.
