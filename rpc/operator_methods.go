package rpc

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Operator-only RPC method table.
//
// Until now, the ONLY place this deny-list existed was nhbportal's browser
// gateway (src/routes/api/rpc/operatorMethods.ts), which sits in front of
// this node for browser traffic. Any caller that reaches this node directly
// -- an off-chain daemon with its own node JWT, a misconfigured client, a
// compromised credential -- was never subject to it: requireAuth only asked
// "is this credential valid", never "what is this caller allowed to do". A
// daemon's leaked or buggy credential could therefore call net_ban,
// sync_snapshot_import, swap_setManualQuote and every other method below
// with nothing at the node to stop it.
//
// OPERATOR_ONLY_METHODS and OPERATOR_ONLY_METHOD_PREFIXES below are a
// deliberate 1:1 port of nhbportal's table (down to the comments explaining
// why each entry is here), made authoritative at the node itself so
// enforcement no longer depends on every caller routing through the portal
// gateway. The two lists are NOT wired together in code -- they live in
// separate repos -- so they CAN drift apart silently. If a method is added
// to either list, add it to the other in the same change:
//   - node (authoritative for enforcement):   rpc/operator_methods.go (this file)
//   - gateway (authoritative for the browser): nhbportal: src/routes/api/rpc/operatorMethods.ts
//
// See requireOperatorInto/handle in rpc/http.go for how this table is
// enforced: any request naming one of these methods must authenticate with
// a credential carrying the 'operator' role (see jwtClaims.Role and
// ServerConfig.OperatorClientCertOU), regardless of which gateway, if any,
// the request came through.
//
// One deliberate exception: handle() does not enforce this for a method
// isPublicSwapMethod also claims (swap_submitVoucher, the swap_voucher_*
// family, nhb_swapMint, nhb_swapBurn). Those are listed here because
// nhbportal never wants a browser session calling them, not because this
// node treats them as an operator surface -- they already carry their own,
// longer-standing per-partner HMAC API key authorization
// (authenticateSwapRequest), which is a stronger, more specific control than
// the shared operator role would add. See the comment at that exemption in
// rpc/http.go's handle for the full reasoning.
//
// A second, separately-checked exception (isSelfAuthenticatedMintMethod):
// mint_with_sig. It is listed here because nhbportal never wants a browser
// session calling it either, not because this node treats it as an operator
// surface -- it already carries its own on-chain secp256k1 voucher-signature
// check plus a MINTER_NHB role requirement (core/state_transition.go's
// applyMintTransaction), which the real, currently-deployed off-chain
// fiat-settlement minting service relies on with no bearer credential at
// all. See isSelfAuthenticatedMintMethod's doc comment in rpc/http.go for
// why this is a separate check rather than folded into isPublicSwapMethod.
//
// A third, separately-checked exception (isSignatureThresholdAuthorizedMethod):
// buyback_submitRefPrice and lending_submitRefPrice. Unlike mint_with_sig,
// their case arms in rpc/http.go's handle() already call requireAuthInto, so
// a valid credential is still required -- only the additional operator-role
// layer is skipped. Their real, independent control is an on-chain M-of-N
// signature-threshold check against a genesis-declared signer quorum
// (core/buyback_tx.go's applyBuybackRefPrice, core/lending_tx.go's
// applyLendingRefPriceTransaction), which the real, currently-deployed
// off-chain reference-price submission service relies on without an
// operator-scoped credential. See isSignatureThresholdAuthorizedMethod's doc
// comment in rpc/http.go for the full reasoning.
var OperatorOnlyMethods = map[string]struct{}{
	// --- p2p network control and node-internal sync (also covered by prefixes below)
	"net_info":             {},
	"net_peers":            {},
	"net_dial":             {}, // makes the validator dial an arbitrary target
	"net_ban":              {}, // bans a peer for N seconds; on a 2-validator chain this halts consensus
	"p2p_info":             {}, // p2p network view (node id, listen addrs, peer counts)
	"p2p_peers":            {},
	"sync_snapshot_export": {}, // writes a snapshot to a caller-supplied filesystem path
	"sync_snapshot_import": {}, // replaces state from a caller-supplied filesystem path
	"sync_status":          {},

	// --- swap engine / voucher / redemption operations
	"swap_setManualQuote":         {}, // sets the oracle-free manual swap quote
	"swap_voucher_reverse":        {},
	"swap_markReconciled":         {},
	"swap_limits":                 {},
	"swap_provider_status":        {},
	"swap_burn_list":              {},
	"swap_listPendingRedemptions": {}, // every account's pending redemptions and payout addresses
	"swap_submitVoucher":          {}, // mints against a partner voucher
	"swap_voucher_get":            {},
	"swap_voucher_list":           {},
	"swap_voucher_export":         {},
	"nhb_swapMint":                {}, // stable-engine reservation for a caller-supplied account
	"nhb_swapBurn":                {}, // stable-engine cash-out
	"mint_with_sig":               {}, // NHB mint via signed voucher; exempt from the gate below, see isSelfAuthenticatedMintMethod

	// --- oracle / reference-price submission
	"buyback_submitRefPrice": {}, // M-of-N signed ref price; exempt from the gate below, see isSignatureThresholdAuthorizedMethod
	"lending_submitRefPrice": {}, // M-of-N signed ref price; exempt from the gate below, see isSignatureThresholdAuthorizedMethod

	// --- paymaster and POS operations
	"tx_setSponsorshipEnabled": {},
	"pos_sweepVoids":           {},

	// --- POTSO / engagement / reputation writers and admin exports
	"potso_submitEvidence":        {},
	"potso_reward_claim":          {}, // pays out of the reward treasury
	"potso_heartbeat":             {},
	"potso_export_epoch":          {}, // full epoch payout CSV
	"potso_rewards_outflow":       {},
	"engagement_register_device":  {},
	"engagement_submit_heartbeat": {},
	"reputation_verifySkill":      {}, // takes a caller-supplied verifier address

	// --- admin analytics (the portal's own admin pages read these server-side)
	"nhb_getOwnerWalletStats": {},
	"nhb_txWindowStats":       {},

	// --- escrow milestone writers (not called by the portal)
	"escrow_milestoneCreate":             {},
	"escrow_milestoneFund":               {},
	"escrow_milestoneRelease":            {},
	"escrow_milestoneCancel":             {},
	"escrow_milestoneSubscriptionUpdate": {},

	// --- legacy direct-state mutators. The current node answers these with 410
	// Gone (they wrote validator-local state outside the block pipeline); the
	// portal signs real transactions through nhb_sendTransaction instead. Denied
	// here in case a stale node build still serves them.
	"lending_supplyNHB":        {},
	"lending_withdrawNHB":      {},
	"lending_depositZNHB":      {},
	"lending_withdrawZNHB":     {},
	"lending_borrowNHB":        {},
	"lending_borrowNHBWithFee": {},
	"lending_repayNHB":         {},
	"lending_liquidate":        {},
	"stake_delegate":           {},
	"stake_undelegate":         {},
	"stake_claim":              {},
	"stake_claimRewards":       {},
	"loyalty_createBusiness":   {},
	"loyalty_setPaymaster":     {},
	"loyalty_addMerchant":      {},
	"loyalty_removeMerchant":   {},
	"loyalty_createProgram":    {},
	"loyalty_updateProgram":    {},
	"loyalty_pauseProgram":     {},
	"loyalty_resumeProgram":    {},
	"creator_publish":          {},
	"creator_tip":              {},
	"creator_stake":            {},
	"creator_unstake":          {},
	"creator_payouts":          {},
	"identity_setAlias":        {},
	"identity_setAvatar":       {},
	"identity_addAddress":      {},
	"identity_removeAddress":   {},
	"identity_setPrimary":      {},
	"identity_rename":          {},
	"identity_createClaimable": {},
	"identity_claim":           {},
	"claimable_create":         {},
	"claimable_claim":          {},
	"claimable_cancel":         {},
	"escrow_create":            {},
	"escrow_fund":              {},
	"escrow_release":           {},
	"escrow_refund":            {},
	"escrow_dispute":           {},
	"escrow_expire":            {},
	"escrow_resolve":           {},
	"p2p_createTrade":          {},
	"p2p_settle":               {},
	"p2p_dispute":              {},
	"p2p_resolve":              {},

	// --- methods the node has already removed (governance, pool creation, POTSO
	// staking now go through signed transactions). Denied so a stale node build
	// cannot be driven with the portal's service JWT.
	"lend_createPool":      {},
	"lending_createPool":   {},
	"gov_propose":          {},
	"gov_vote":             {},
	"gov_finalize":         {},
	"gov_queue":            {},
	"gov_execute":          {},
	"potso_stake_lock":     {},
	"potso_stake_unbond":   {},
	"potso_stake_withdraw": {},
}

// OperatorOnlyMethodPrefixes mirrors nhbportal's whole-namespace deny list.
// net_ and sync_ are this node's live ones; the rest are conventional
// operator namespaces the node does not serve today, listed so a future
// node build cannot expose them through a wildcard allowlist or a stale
// client.
var OperatorOnlyMethodPrefixes = []string{
	"net_",
	"sync_",
	"admin_",
	"debug_",
	"operator_",
	"personal_",
	"miner_",
}

var normalizedOperatorOnlyMethods = buildNormalizedOperatorOnlyMethods()

func buildNormalizedOperatorOnlyMethods() map[string]struct{} {
	out := make(map[string]struct{}, len(OperatorOnlyMethods))
	for method := range OperatorOnlyMethods {
		out[NormalizeRPCMethodName(method)] = struct{}{}
	}
	return out
}

// NormalizeRPCMethodName puts a method name into the same strict, canonical
// form nhbportal's gateway uses before consulting its deny-list: NFKC folds
// compatibility characters (fullwidth letters and the like), every
// whitespace, control, format (zero-width, bidi, soft hyphen, BOM) and
// variation-selector character is removed anywhere in the name (not just at
// the ends), and the result is lowercased. This catches padded, re-cased or
// invisibly-decorated variants of a denied method name (e.g. a zero-width
// space spliced into "net_ban", or a fullwidth variant of the same letters)
// that would otherwise slip past a naive exact-match check.
func NormalizeRPCMethodName(method string) string {
	normalized := norm.NFKC.String(method)
	var b strings.Builder
	b.Grow(len(normalized))
	for _, r := range normalized {
		if isStrippedRPCMethodNameRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// isStrippedRPCMethodNameRune reports whether r is one of the characters
// NormalizeRPCMethodName removes: any Unicode control (Cc) or format (Cf)
// character, any separator (Zs/Zl/Zp, i.e. general category Z), the
// Mongolian vowel separator U+180E (reclassified out of Zs in Unicode 6.3
// but still whitespace-like for this purpose), and the variation-selector
// block U+FE00-U+FE0F.
func isStrippedRPCMethodNameRune(r rune) bool {
	if r == 0x180e {
		return true
	}
	if r >= 0xfe00 && r <= 0xfe0f {
		return true
	}
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zs, unicode.Zl, unicode.Zp)
}

// IsOperatorOnlyMethod reports whether method -- in whatever casing, padding
// or decoration a caller sent it -- names an operator-only RPC method or
// falls under an operator-only namespace prefix. It is the node's own
// authoritative check, independent of whether the request arrived through
// nhbportal's gateway, another gateway, or directly from a daemon.
func IsOperatorOnlyMethod(method string) bool {
	normalized := NormalizeRPCMethodName(method)
	if normalized == "" {
		return false
	}
	if _, ok := normalizedOperatorOnlyMethods[normalized]; ok {
		return true
	}
	for _, prefix := range OperatorOnlyMethodPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}
