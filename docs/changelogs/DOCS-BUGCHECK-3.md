# DOCS-BUGCHECK-3 — Publish bugcheck reports in docs site

- add automated publication script that syncs the newest `audit/bugcheck-*.md` report into `docs/audit/latest.md`, archives history, and keeps navigation metadata current.
- expose a bugcheck history page with PASS/FAIL gate guidance and append-only run index.
- integrate the publication step into CI and ship generated docs as build artifacts.
