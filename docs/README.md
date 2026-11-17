# NHBChain documentation

The Markdown sources of the developer documentation live in this directory. Start with the repository [README](../README.md), then [index.md](./index.md), which lists the runnable components.

## Layout

- [`_toc.yaml`](./_toc.yaml) is a navigation tree with five top-level entries: Overview (`index.md`), Point of Sale (specifications, APIs and runbooks), Fees (`fees/policy.md`, `fees/routing.md`, `governance/fee-params.md`, `ops/fees.md`), Security (`security/mint-controls.md`) and Wallets (`wallet/key-management.md`). Every path in it exists. No tool in this repository reads the file, so pages not listed there are still part of the documentation.
- Topic directories under `docs/`: `api`, `architecture`, `audit`, `changelogs`, `cli`, `commerce`, `consensus`, `consensusd`, `cookbooks`, `creator`, `deploy`, `dev`, `escrow`, `examples`, `fees`, `finance`, `gateway`, `gov`, `governance`, `identity`, `integration`, `launch`, `lending`, `loyalty`, `migrate`, `networking`, `openapi`, `ops`, `oracle`, `overview`, `p2p`, `perf`, `potso`, `queries`, `reputation`, `runbooks`, `sdk`, `security`, `services`, `slas`, `specs`, `staking`, `subscriptions`, `swap`, `testing`, `tokenomics`, `transactions`, `transparency`, `treasury`, `validators`, `wallet`. Loose pages: `api.md`, `potso_rewards.md`, `CHANGELOG.md`.
- Fee policy pages: [fee policy](./fees/policy.md) and [fee routing](./fees/routing.md).

## Verification

`tools/docs/snippets` provides one checker, `snippets.Verify`, run over the Markdown files under a root directory (default `docs`). All of these commands call it, from the repository root:

```bash
go run ./scripts/verify-docs-snippets        # flag: -root <dir>, default docs
go run ./tools/docs/verify.go
make docs:verify
```

`make audit:docs` runs `tools/docs/verify.go` and then the audit phase script, `scripts/audit.sh` runs `make docs:verify`, and `scripts/bugcheck.sh` runs `make bugcheck-docs`, which calls `make audit:docs`.

For every `.md` file the checker does three things:

1. **Embeds.** A line of the form `<!-- embed:PATH -->` must be followed by a fenced code block. The block must match the file at `PATH` (resolved from the working directory) after trailing carriage returns and trailing newlines are ignored; otherwise the check fails with `embed PATH out of sync`. The snippets in `cookbooks/developers.md` embed files under `examples/docs/` and `examples/queries/`.
2. **Compilation.** Each embedded Go file is built with `go build -o <null device> FILE`. If any TypeScript snippet is embedded, `npx tsc --noEmit --project examples/docs/tsconfig.json` runs once.
3. **Relative links.** Every inline Markdown link or image (text in square brackets followed by a target in parentheses) whose target is not `http(s)`, `mailto`, `tel`, `data:`, an anchor-only `#...` or a path starting with `/` must point at an existing file. A `#anchor` suffix is removed and not checked. Reference-style links and HTML links are not checked.

External links are not checked by that tool. The following command uses `markdown-link-check` through `npx` with the config in [`.markdown-link-check.json`](./.markdown-link-check.json), which ignores `mailto:` and `tel:` links; no script in the repository calls it:

```bash
find docs -name '*.md' -print0 | \
  xargs -0 -n1 -I{} npx --yes markdown-link-check --quiet \
    --config docs/.markdown-link-check.json {}
```
