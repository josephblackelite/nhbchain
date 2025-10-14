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
