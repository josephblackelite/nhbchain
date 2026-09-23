# DOCS-BUGCHECK-3 — Publish bugcheck reports in docs site

- add automated publication script that syncs the newest `audit/report-*.md` or `audit/bugcheck-*.md` report (by modification time; `scripts/bugcheck.sh` writes the reports, and the repo tracks only `audit/.gitkeep`) into `docs/audit/latest.md`, archives history, and keeps navigation metadata current.
- expose a bugcheck history page with PASS/FAIL gate guidance and a run index (`scripts/publish_bugcheck.sh` inserts each new link directly after a marker line, so the newest run is listed first; rows are not removed).
- integrate the publication step into CI and ship generated docs as build artifacts.
