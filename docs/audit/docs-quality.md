# Documentation Quality Review Guide

Documentation is part of what an auditor reproduces, so the repository has an automated check for a subset of documentation defects, and a manual checklist for the rest.

## Automated check

```bash
make audit:docs
# or, directly:
go run ./tools/docs/verify.go
go run ./scripts/verify-docs-snippets --root docs
```

Both entry points call `snippets.Verify` (`tools/docs/snippets/snippets.go`) on the `docs/` tree. It:

- reads every `.md` file under the root;
- for each `<!-- embed:path -->` marker followed by a code fence, compares the fenced block with the referenced file and fails with `out of sync` if they differ;
- runs `go build` on the referenced file for every embed fenced as `go`, and `npx tsc --noEmit --project examples/docs/tsconfig.json` if any embed is fenced as `ts` or `typescript`;
- checks that the target of every Markdown link or image with a relative path resolves to an existing file. The scan is a regular expression over the raw text, so it also matches link syntax inside code spans. `http(s)`, `mailto`, `tel`, `data:` and `#anchor` targets and site-absolute paths starting with `/` are skipped, and anchors are not checked.

`make audit:docs` then runs `scripts/audit/run_phase.sh docs ops/audit/docs.yaml artifacts/docs`, which hashes `docs/security/audit-readiness.md` and `ops/audit-pack/BUILD_STEPS.md` into the phase report; that command fails if either file is missing. `ops/audit-pack/BUILD_STEPS.md` does not exist in the repository, so `make audit:docs` (and therefore `make bugcheck-docs` and the `docs` bugcheck) currently fails at this step even when the snippet check passes. The snippet check on its own (`go run ./tools/docs/verify.go`) does not depend on that file.

The check does not verify that prose statements are true. That is the manual part.

## Manual review

For each document sampled:

1. **Accuracy.** Compare every command, flag, configuration key, default value and RPC method name with the code that implements it. Treat a statement you cannot find in code as unverified.
2. **Working examples.** Run the commands. Where a document refers to a file or target, confirm it exists (for example, that the Makefile target is defined).
3. **Links.** The automated check covers relative links; confirm external links by hand.
4. **Scope.** Documents must describe on-chain and node behavior implemented in this repository. Remove statements about planned features.

The `docs/` tree has these directories that hold operator and developer material: `docs/runbooks/`, `docs/ops/`, `docs/architecture/`, `docs/consensus/`, `docs/overview/`, `docs/sdk/`. There is no `README.md` directly inside any `services/<name>/` directory.

## Exit criteria

- `go run ./tools/docs/verify.go` succeeds on the commit under review (`make audit:docs` also needs `ops/audit-pack/BUILD_STEPS.md`, which is missing).
- Each sampled document was checked against the code and any discrepancy was corrected or filed as an issue.
