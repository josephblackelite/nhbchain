package rpc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every case of the dispatch switch in Server.handle (rpc/http.go) is either
// operator-only (OperatorOnlyMethods, a deny-list) or listed below. The two
// lists below are the methods that are NOT operator-only. Each one was classified
// by a person, either as an open read or as a method whose own arm authenticates
// the caller (see the notes in operator_methods.go for the exemptions).
//
// TestEveryDispatchCaseIsClassified fails when handle() gains a case that is in
// none of these lists. That is the point: the operator table is a deny-list, so a
// method added to the switch is open to every caller by default, and nothing else
// fails the build when a new one is forgotten. Adding a name below is a statement
// that its arm was read and that being open to every caller (or authenticating
// inside its own arm) is intended.

// openReadDispatchMethods lists every dispatch case that is NOT operator-only and whose arm does not
// authenticate the caller: a read, quote or preview method that anyone who reaches the node may call.
// Each name was read when the list was made (2026-11-01); the names all describe reads.
var openReadDispatchMethods = map[string]struct{}{
	"buyback_getRefPriceStatus":         {},
	"claimable_get":                     {},
	"escrow_get":                        {},
	"escrow_getRealm":                   {},
	"escrow_getSnapshot":                {},
	"escrow_listEvents":                 {},
	"escrow_milestoneGet":               {},
	"fees_getMonthlyStatus":             {},
	"fees_getTransferQuote":             {},
	"fees_getTransferSchedule":          {},
	"fees_getTransferStatus":            {},
	"fees_listTotals":                   {},
	"gov_list":                          {},
	"gov_params":                        {},
	"bridge_getStatus":                  {},
	"amm_getPool":                       {},
	"amm_quote":                         {},
	"amm_getTrades":                     {},
	"amm_getTicker":                     {},
	"market_getTicker":                  {},
	"market_getRecentFills":             {},
	"market_getDailyStats":              {},
	"gov_proposal":                      {},
	"identity_resolve":                  {},
	"identity_reverse":                  {},
	"lend_getPools":                     {},
	"lending_getFixedTermDeposit":       {},
	"lending_getFixedTermLoan":          {},
	"lending_getMarket":                 {},
	"lending_getRateSchedule":           {},
	"lending_getRefPriceStatus":         {},
	"lending_getUserAccount":            {},
	"loyalty_getBusiness":               {},
	"loyalty_listAccruals":              {},
	"loyalty_listBusinesses":            {},
	"loyalty_listPrograms":              {},
	"loyalty_paymasterBalance":          {},
	"loyalty_programStats":              {},
	"loyalty_resolveUsername":           {},
	"loyalty_userDaily":                 {},
	"loyalty_userQR":                    {},
	"market_getFlatFee":                 {},
	"market_getListing":                 {},
	"market_getMyFills":                 {},
	"market_getMyListings":              {},
	"market_listOpenListings":           {},
	"nhb_getAddressActivity":            {},
	"nhb_getBalance":                    {},
	"nhb_getEpochSnapshot":              {},
	"nhb_getEpochSummary":               {},
	"nhb_getExplorerSnapshot":           {},
	"nhb_getLatestBlocks":               {},
	"nhb_getLatestTransactions":         {},
	"nhb_getLoyaltyBudgetStatus":        {},
	"nhb_getNetworkStats":               {},
	"nhb_getOraclePrice":                {},
	"nhb_getRewardEpoch":                {},
	"nhb_getRewardHistory":              {},
	"nhb_getRewardPayout":               {},
	"nhb_getSlashingEvents":             {},
	"nhb_getTotalSupply":                {},
	"nhb_getTransaction":                {},
	"nhb_getTransactionHistory":         {},
	"nhb_getTransactionReceipt":         {},
	"nhb_getValidatorInfo":              {},
	"nhb_getValidatorSet":               {},
	"nhb_searchExplorer":                {},
	"p2p_getTrade":                      {},
	"pos_getAuthorization":              {},
	"pos_getAuthorizationByIntentRef":   {},
	"potso_epoch_info":                  {},
	"potso_epoch_payouts":               {},
	"potso_getEvidence":                 {},
	"potso_getWeight":                   {},
	"potso_leaderboard":                 {},
	"potso_listEvidence":                {},
	"potso_params":                      {},
	"potso_rewards_history":             {},
	"potso_top":                         {},
	"potso_userMeters":                  {},
	"stake_getPosition":                 {},
	"stake_previewClaim":                {},
	"subscriptions_getConfig":           {},
	"subscriptions_getPlan":             {},
	"subscriptions_getSubscription":     {},
	"subscriptions_listByMerchant":      {},
	"subscriptions_listByPayer":         {},
	"subscriptions_listCharges":         {},
	"subscriptions_listPlansByMerchant": {},
	"swap_getRedemptionFeeParams":       {},
	"swap_getRiskParams":                {},
	"tx_getSponsorshipConfig":           {},
	"tx_previewSponsorship":             {},
	"znhb_getTokenomicsState":           {},
	"znhb_quoteBuy":                     {},
}

// authenticatingDispatchMethods lists the non-operator dispatch cases whose own arm calls requireAuthInto
// (a write, or a read tied to the caller): the test fails if one of them loses that call.
var authenticatingDispatchMethods = map[string]struct{}{
	"nhb_checkSwapAllowance":  {},
	"nhb_getSwapQuote":        {},
	"nhb_getSwapStatus":       {},
	"nhb_requestSwapApproval": {},
	"nhb_sendTransaction":     {},
	"potso_stake_info":        {},
}

// armCallsAuth reports, for each string case label of the dispatch switch, whether
// the body of its case arm calls requireAuthInto or requireAuth (the arm
// authenticates the caller itself). It is filled by dispatchCaseLabels.
var armCallsAuth = map[string]bool{}

// dispatchCaseLabels returns every string case label of the dispatch switch on
// req.Method inside Server.handle, plus the labels written as something other
// than a string literal.
func dispatchCaseLabels(t *testing.T) (labels []string, nonLiteral []string) {
	t.Helper()
	armCallsAuth = map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "http.go", nil, 0)
	if err != nil {
		t.Fatalf("parse http.go: %v", err)
	}
	var handle *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handle" || fn.Recv == nil {
			continue
		}
		handle = fn
		break
	}
	if handle == nil {
		t.Fatal("Server.handle not found in http.go")
	}
	found := false
	ast.Inspect(handle.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		sel, ok := sw.Tag.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Method" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "req" {
			return true
		}
		found = true
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			authed := false
			for _, body := range clause.Body {
				ast.Inspect(body, func(m ast.Node) bool {
					call, ok := m.(*ast.CallExpr)
					if !ok {
						return true
					}
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "requireAuthInto" || sel.Sel.Name == "requireAuth") {
						authed = true
					}
					return true
				})
			}
			for _, expr := range clause.List {
				if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if value, err := strconv.Unquote(lit.Value); err == nil {
						armCallsAuth[value] = authed
					}
				}
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					nonLiteral = append(nonLiteral, fmt.Sprintf("%T at %s", expr, fset.Position(expr.Pos())))
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", lit.Value, err)
				}
				labels = append(labels, value)
			}
		}
		return false
	})
	if !found {
		t.Fatal("the dispatch switch on req.Method was not found in Server.handle")
	}
	sort.Strings(labels)
	return labels, nonLiteral
}

func TestEveryDispatchCaseIsClassified(t *testing.T) {
	labels, nonLiteral := dispatchCaseLabels(t)
	if len(nonLiteral) > 0 {
		t.Fatalf("dispatch case labels that are not string literals cannot be classified by this test: %s", strings.Join(nonLiteral, "; "))
	}
	if len(labels) < 100 {
		t.Fatalf("only %d case labels found in the dispatch switch; the test is no longer reading it", len(labels))
	}

	var unclassified []string
	seen := map[string]struct{}{}
	for _, label := range labels {
		if _, dup := seen[label]; dup {
			t.Errorf("method %q appears in two case labels of the dispatch switch", label)
		}
		seen[label] = struct{}{}
		_, openRead := openReadDispatchMethods[label]
		_, authenticating := authenticatingDispatchMethods[label]
		if openRead && authenticating {
			t.Errorf("method %q is in both openReadDispatchMethods and authenticatingDispatchMethods", label)
		}
		if IsOperatorOnlyMethod(label) {
			if openRead || authenticating {
				t.Errorf("method %q is operator-only and also in a public list; remove it from the public list", label)
			}
			continue
		}
		if !openRead && !authenticating {
			unclassified = append(unclassified, label)
			continue
		}
		if authenticating && !armCallsAuth[label] {
			t.Errorf("method %q is listed as authenticating inside its own arm, but the arm no longer calls requireAuthInto: it is now open to every caller", label)
		}
		if openRead && !readLikeMethodName.MatchString(label) {
			t.Errorf("method %q is listed as an open read but its name does not describe a read; if it changes anything it must authenticate and belong in authenticatingDispatchMethods", label)
		}
	}

	if os.Getenv("NHB_PRINT_DISPATCH") != "" {
		var open, authed strings.Builder
		for _, label := range unclassified {
			if armCallsAuth[label] {
				fmt.Fprintf(&authed, "\t%q: {},\n", label)
			} else {
				fmt.Fprintf(&open, "\t%q: {},\n", label)
			}
		}
		t.Logf("ARMS THAT AUTHENTICATE THE CALLER THEMSELVES:\n%s", authed.String())
		t.Logf("ARMS THAT DO NOT (open to any caller who reaches the node):\n%s", open.String())
		return
	}
	if len(unclassified) > 0 {
		t.Fatalf("these dispatch cases are neither operator-only (operator_methods.go) nor classified in this file: %s\n"+
			"The operator table is a deny-list, so a new case is open to every caller until it is classified. Read the new arm: if it must be operator-only add it to OperatorOnlyMethods "+
			"(and the gateway's table); if it is an open read add it to openReadDispatchMethods; if it authenticates inside its own arm add it to authenticatingDispatchMethods.", strings.Join(unclassified, ", "))
	}

	for _, list := range []struct {
		name  string
		names map[string]struct{}
	}{{"openReadDispatchMethods", openReadDispatchMethods}, {"authenticatingDispatchMethods", authenticatingDispatchMethods}} {
		for name := range list.names {
			if _, ok := seen[name]; !ok {
				t.Errorf("%s lists %q, which has no case in the dispatch switch any more; remove it", list.name, name)
			}
		}
	}
}

// readLikeMethodName matches the names of methods that only read, quote or
// preview. An open-read method whose name does not match must be looked at.
var readLikeMethodName = regexp.MustCompile(`(?i)(get|list|quote|preview|resolve|reverse|_info|_params|_top$|leaderboard|stats|summary|snapshot|status|search|proposal$|^claimable_get$|^escrow_get$|balance|programstats|userdaily|userqr|usermeters|epoch_payouts|rewards_history)`)
