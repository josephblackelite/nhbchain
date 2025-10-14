# NHBChain documentation

The documentation site is generated from the Markdown sources in this directory.
Navigation is controlled by [`_toc.yaml`](./_toc.yaml); update it whenever new
pages should appear in the rendered sidebar. Group related content under the
Point of Sale sections so that operators can discover the specifications,
reference APIs, and runbooks in a single place.

For fee policy specifics, see the new [fee policy](./fees/policy.md) and [fee routing](./fees/routing.md) guides.

## Verification workflow

Two automated checks keep the docs healthy:

1. **Snippet verification** &mdash; embeds copy code from the `examples/docs`
   directory into Markdown. Run the verifier to ensure every fenced block stays
   synchronised and still compiles:

  ```bash
  go run ./scripts/verify-docs-snippets
  ```

2. **Markdown link checking** &mdash; validate external and intra-site links before
   landing a change:

   ```bash
   find docs -name '*.md' -print0 | \
     xargs -0 -n1 -I{} npx --yes markdown-link-check --quiet \
       --config docs/.markdown-link-check.json {}
   ```

The snippet verifier enforces that all fenced code blocks generated via
`<!-- embed:... -->` comments match their source files. It compiles Go examples
with `go build` and type-checks the TypeScript snippets with `tsc`, failing when
any drift is detected.

## Acceptance

Feature work that touches the POS docs should meet the following acceptance
criteria before shipping:

> Docs appear in nav; snippet & link checks green.

Tracking the checks locally with the commands above ensures we satisfy the
release gate prior to opening a pull request.
