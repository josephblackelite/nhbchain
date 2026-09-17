# Latest bugcheck report

> ℹ️ This file is automatically updated by `scripts/publish_bugcheck.sh` after each CI run of the bugcheck pipeline.
>
> Run `bash scripts/publish_bugcheck.sh` locally once a bugcheck report exists in `audit/` to refresh this page.

## English status report — 2025-10-14T05:43:41Z

- ❌ Unit tests (`go test ./...`)
  Proofs:
    - `TestNodeIdentityAddressManagement` · `/workspace/nhbchain/core/identity_integration_test.go:123` — func TestNodeIdentityAddressManagement(t *testing.T) {
    - `TestLoyaltyEngineAppliesBaseAndProgramRewards` · `/workspace/nhbchain/core/loyalty_integration_test.go:21` — func TestLoyaltyEngineAppliesBaseAndProgramRewards(t *testing.T) {
    - `TestHandleIdentityAddressManagement` · `/workspace/nhbchain/rpc/identity_handlers_test.go:239` — func TestHandleIdentityAddressManagement(t *testing.T) {
  Notes:
    - Failing tests: nhbchain/core.TestNodeIdentityAddressManagement, nhbchain/core.TestLoyaltyEngineAppliesBaseAndProgramRewards, nhbchain/rpc.TestHandleIdentityAddressManagement

- ✅ Documentation snippets (`go run ./scripts/verify-docs-snippets --root docs`)
  Proofs:
    - `func Verify(docRoot string) error {` · `/workspace/nhbchain/tools/docs/snippets/snippets.go:26` — func Verify(docRoot string) error {
    - `if !strings.HasPrefix(line, "<!-- embed:") || !strings.HasSuffix(line, "-->") {` · `/workspace/nhbchain/tools/docs/snippets/snippets.go:68` — if !strings.HasPrefix(line, "<!-- embed:") || !strings.HasSuffix(line, "-->") {
    - `return fmt.Errorf("broken relative link %q", target)` · `/workspace/nhbchain/tools/docs/snippets/snippets.go:194` — return fmt.Errorf("broken relative link %q", target)

## English status report — 2025-10-14T07:59:05Z

- ❌ Unit tests (`go test ./...`)
  Proofs:
    - `TestStakeClaim_NotReady` · `rpc/stake_handlers_test.go:21` — func TestStakeClaim_NotReady(t *testing.T) {
    - `TestStableRPCHandlersFlow` · `rpc/swap_stable_handlers_test.go:20` — func TestStableRPCHandlersFlow(t *testing.T) {
    - `TestLendingRPCEndpoints` · `tests/e2e/lending_rpc_test.go:37` — func TestLendingRPCEndpoints(t *testing.T) {
  Notes:
    - Failing tests: nhbchain/rpc.TestStakeClaim_NotReady, nhbchain/rpc.TestStableRPCHandlersFlow, nhbchain/tests/e2e.TestLendingRPCEndpoints

- ✅ Documentation snippets (`go run ./scripts/verify-docs-snippets --root docs`)
  Proofs:
    - `func Verify(docRoot string) error {` · `tools/docs/snippets/snippets.go:26` — func Verify(docRoot string) error {
    - `return fmt.Errorf("broken relative link %q", target)` · `tools/docs/snippets/snippets.go:194` — return fmt.Errorf("broken relative link %q", target)
    - `snippets = append(snippets, snippet{lang: lang, file: embedPath})` · `tools/docs/snippets/snippets.go:105` — snippets = append(snippets, snippet{lang: lang, file: embedPath})
