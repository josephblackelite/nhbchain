package rpc

import "strings"

// unknownRPCLabel is how a method name this server does not serve is reported
// in the request, throttle and limiter-hit metrics. The method name comes from
// the request body and is read before any check on it, so using it as a metric
// label would give every distinct string the caller sends its own series for
// the life of the process.
const unknownRPCLabel = "unknown"

// unknownLimiterRoute is the route that every method name the server does not
// serve (and that has no route limit of its own) shares in the rate limiter.
// It starts with a character no served method name can start with, so it never
// collides with one.
const unknownLimiterRoute = "?unknown"

// knownRPCMethods is the set of method names Server.handle dispatches, that is,
// the string case labels of the switch on req.Method there. A name outside the
// set is answered with "method not found". TestKnownRPCMethodsMatchDispatchSwitch
// fails when this set and that switch disagree in either direction, so a method
// added to the switch has to be added here too.
var knownRPCMethods = map[string]struct{}{
	"nhb_sendTransaction":                {},
	"tx_previewSponsorship":              {},
	"tx_getSponsorshipConfig":            {},
	"tx_setSponsorshipEnabled":           {},
	"nhb_getBalance":                     {},
	"nhb_getTotalSupply":                 {},
	"nhb_getLatestBlocks":                {},
	"nhb_getLatestTransactions":          {},
	"nhb_getTransactionHistory":          {},
	"nhb_getAddressActivity":             {},
	"nhb_getTransaction":                 {},
	"nhb_getTransactionReceipt":          {},
	"nhb_searchExplorer":                 {},
	"nhb_getExplorerSnapshot":            {},
	"nhb_getEpochSummary":                {},
	"nhb_getEpochSnapshot":               {},
	"nhb_getRewardEpoch":                 {},
	"nhb_getRewardPayout":                {},
	"nhb_getRewardHistory":               {},
	"mint_with_sig":                      {},
	"swap_getRiskParams":                 {},
	"swap_getRedemptionFeeParams":        {},
	"swap_submitVoucher":                 {},
	"swap_voucher_get":                   {},
	"swap_voucher_list":                  {},
	"swap_voucher_export":                {},
	"nhb_requestSwapApproval":            {},
	"nhb_getSwapQuote":                   {},
	"nhb_swapMint":                       {},
	"nhb_swapBurn":                       {},
	"nhb_getSwapStatus":                  {},
	"nhb_checkSwapAllowance":             {},
	"nhb_getLoyaltyBudgetStatus":         {},
	"nhb_getOwnerWalletStats":            {},
	"nhb_getSlashingEvents":              {},
	"nhb_getValidatorSet":                {},
	"nhb_getValidatorInfo":               {},
	"nhb_getNetworkStats":                {},
	"nhb_getOraclePrice":                 {},
	"znhb_getTokenomicsState":            {},
	"znhb_quoteBuy":                      {},
	"buyback_getRefPriceStatus":          {},
	"buyback_submitRefPrice":             {},
	"lending_getRefPriceStatus":          {},
	"lending_submitRefPrice":             {},
	"fees_listTotals":                    {},
	"fees_getMonthlyStatus":              {},
	"fees_getTransferStatus":             {},
	"fees_getTransferQuote":              {},
	"subscriptions_getPlan":              {},
	"subscriptions_listPlansByMerchant":  {},
	"subscriptions_getSubscription":      {},
	"subscriptions_listByPayer":          {},
	"subscriptions_listByMerchant":       {},
	"subscriptions_listCharges":          {},
	"subscriptions_getConfig":            {},
	"swap_limits":                        {},
	"swap_provider_status":               {},
	"swap_burn_list":                     {},
	"swap_voucher_reverse":               {},
	"swap_markReconciled":                {},
	"swap_setManualQuote":                {},
	"swap_listPendingRedemptions":        {},
	"pos_sweepVoids":                     {},
	"pos_getAuthorization":               {},
	"pos_getAuthorizationByIntentRef":    {},
	"lending_getMarket":                  {},
	"lend_getPools":                      {},
	"lending_getUserAccount":             {},
	"lending_getFixedTermLoan":           {},
	"lending_getFixedTermDeposit":        {},
	"lending_getRateSchedule":            {},
	"lending_supplyNHB":                  {},
	"lending_withdrawNHB":                {},
	"lending_depositZNHB":                {},
	"lending_withdrawZNHB":               {},
	"lending_borrowNHB":                  {},
	"lending_borrowNHBWithFee":           {},
	"lending_repayNHB":                   {},
	"lending_liquidate":                  {},
	"market_listOpenListings":            {},
	"market_getListing":                  {},
	"market_getMyListings":               {},
	"market_getMyFills":                  {},
	"market_getFlatFee":                  {},
	"stake_delegate":                     {},
	"stake_undelegate":                   {},
	"stake_claim":                        {},
	"stake_claimRewards":                 {},
	"stake_getPosition":                  {},
	"stake_previewClaim":                 {},
	"loyalty_createBusiness":             {},
	"loyalty_setPaymaster":               {},
	"loyalty_addMerchant":                {},
	"loyalty_removeMerchant":             {},
	"loyalty_createProgram":              {},
	"loyalty_updateProgram":              {},
	"loyalty_pauseProgram":               {},
	"loyalty_resumeProgram":              {},
	"loyalty_getBusiness":                {},
	"loyalty_listBusinesses":             {},
	"loyalty_listPrograms":               {},
	"loyalty_programStats":               {},
	"loyalty_listAccruals":               {},
	"loyalty_userDaily":                  {},
	"loyalty_paymasterBalance":           {},
	"loyalty_resolveUsername":            {},
	"loyalty_userQR":                     {},
	"creator_publish":                    {},
	"creator_tip":                        {},
	"creator_stake":                      {},
	"creator_unstake":                    {},
	"creator_payouts":                    {},
	"identity_setAlias":                  {},
	"identity_setAvatar":                 {},
	"identity_addAddress":                {},
	"identity_removeAddress":             {},
	"identity_setPrimary":                {},
	"identity_rename":                    {},
	"identity_resolve":                   {},
	"identity_reverse":                   {},
	"identity_createClaimable":           {},
	"identity_claim":                     {},
	"claimable_create":                   {},
	"claimable_claim":                    {},
	"claimable_cancel":                   {},
	"claimable_get":                      {},
	"escrow_create":                      {},
	"escrow_get":                         {},
	"escrow_getRealm":                    {},
	"escrow_getSnapshot":                 {},
	"escrow_listEvents":                  {},
	"escrow_fund":                        {},
	"escrow_release":                     {},
	"escrow_refund":                      {},
	"escrow_expire":                      {},
	"escrow_dispute":                     {},
	"escrow_resolve":                     {},
	"escrow_milestoneCreate":             {},
	"escrow_milestoneGet":                {},
	"escrow_milestoneFund":               {},
	"escrow_milestoneRelease":            {},
	"escrow_milestoneCancel":             {},
	"escrow_milestoneSubscriptionUpdate": {},
	"net_info":                           {},
	"net_peers":                          {},
	"net_dial":                           {},
	"net_ban":                            {},
	"sync_snapshot_export":               {},
	"sync_snapshot_import":               {},
	"sync_status":                        {},
	"p2p_info":                           {},
	"p2p_peers":                          {},
	"p2p_createTrade":                    {},
	"p2p_getTrade":                       {},
	"p2p_settle":                         {},
	"p2p_dispute":                        {},
	"p2p_resolve":                        {},
	"engagement_register_device":         {},
	"engagement_submit_heartbeat":        {},
	"potso_heartbeat":                    {},
	"potso_userMeters":                   {},
	"potso_top":                          {},
	"potso_leaderboard":                  {},
	"potso_params":                       {},
	"potso_getWeight":                    {},
	"potso_stake_info":                   {},
	"potso_epoch_info":                   {},
	"potso_epoch_payouts":                {},
	"potso_reward_claim":                 {},
	"potso_rewards_history":              {},
	"potso_rewards_outflow":              {},
	"nhb_txWindowStats":                  {},
	"potso_export_epoch":                 {},
	"potso_getEvidence":                  {},
	"potso_listEvidence":                 {},
	"gov_proposal":                       {},
	"gov_list":                           {},
	"reputation_verifySkill":             {},
}

// isKnownRPCMethod reports whether method, ignoring surrounding whitespace the
// way the rate limiter and the metrics do, is a name the server dispatches.
func isKnownRPCMethod(method string) bool {
	_, ok := knownRPCMethods[strings.TrimSpace(method)]
	return ok
}

// metricModuleAndMethod is moduleAndMethod for use as metric labels: a method
// the server does not serve is reported as "unknown" instead of under its own
// name, so the number of series stays bounded by the number of served methods
// whatever the callers send. A blank method still yields no module, which the
// caller treats as "record nothing".
func metricModuleAndMethod(method string) (string, string) {
	module, action := moduleAndMethod(method)
	if module == "" {
		return "", ""
	}
	if !isKnownRPCMethod(method) {
		return unknownRPCLabel, unknownRPCLabel
	}
	return module, action
}

// limiterRouteFor returns the route a request is rate limited under. A served
// method, a method with a route limit of its own in the configuration, and a
// blank method are limited under their own name, as before. Any other name
// shares unknownLimiterRoute, so a caller cannot get a new bucket (or a new
// map entry that pushes other callers' buckets out) by changing the name. The
// route limits are read-only once the server is built, so this needs no lock.
func (s *Server) limiterRouteFor(trimmedMethod string) string {
	if trimmedMethod == "" || isKnownRPCMethod(trimmedMethod) {
		return trimmedMethod
	}
	for _, limits := range s.rateLimitMax {
		if _, ok := limits[trimmedMethod]; ok {
			return trimmedMethod
		}
	}
	return unknownLimiterRoute
}
