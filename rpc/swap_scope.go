package rpc

import (
	"net/http"
	"strings"

	swap "nhbchain/native/swap"
)

// swapAllProviders is the entry in a partner's provider list that lets the key
// read the vouchers of every provider.
const swapAllProviders = "*"

// swapPartnerContextKey carries the API key of the partner whose signed request
// was accepted, from the dispatch (which verifies the signature) to the handlers
// of the voucher ledger reads. Only the dispatch sets it, from the verified
// principal, never from anything the caller sent.
const swapPartnerContextKey contextKey = "rpc_swap_partner"

// normalisePartnerProviders turns the configured partner key -> providers lists
// into the sets the read handlers check. Keys are trimmed, provider names are
// compared the way the provider allow-list compares them (trimmed, lower case),
// empty names are dropped and a key left with no provider has no entry at all.
func normalisePartnerProviders(configured map[string][]string) map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{}, len(configured))
	for key, providers := range configured {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		set := make(map[string]struct{}, len(providers))
		for _, provider := range providers {
			name := swap.ProviderKey(provider)
			if name == "" {
				continue
			}
			set[name] = struct{}{}
		}
		if len(set) == 0 {
			continue
		}
		out[trimmedKey] = set
	}
	return out
}

// swapReadScope says which vouchers one caller of swap_voucher_get,
// swap_voucher_list or swap_voucher_export may read.
type swapReadScope struct {
	all       bool
	providers map[string]struct{}
}

// keep is the predicate the ledger reads take: nil (read everything) for a scope
// that sees the whole ledger, otherwise one that accepts a record only when its
// provider is in the scope.
func (sc swapReadScope) keep() func(*swap.VoucherRecord) bool {
	if sc.all {
		return nil
	}
	providers := sc.providers
	return func(record *swap.VoucherRecord) bool {
		if record == nil {
			return false
		}
		_, ok := providers[swap.ProviderKey(record.Provider)]
		return ok
	}
}

// swapReadScopeFor decides what the caller of r may read from the voucher
// ledger, and reports false when it cannot tell who the caller is.
//
//   - With partner authentication configured the caller is the partner whose
//     signed request the dispatch accepted, and reads the providers listed for
//     that key in PartnerProviders: all of them for "*", none for a key with no
//     entry. If no partner identity reached the handler it is refused, so a
//     change to the dispatch can never turn this into a read of the whole ledger.
//   - Without partner authentication the only credential the reads take is the
//     operator's bearer token or client certificate (requireSwapLedgerAuth), and
//     that credential sees the whole ledger. There is no partner to scope to.
func (s *Server) swapReadScopeFor(r *http.Request) (swapReadScope, bool) {
	if s.swapAuth == nil {
		return swapReadScope{all: true}, true
	}
	key, _ := r.Context().Value(swapPartnerContextKey).(string)
	key = strings.TrimSpace(key)
	if key == "" {
		return swapReadScope{}, false
	}
	providers := s.swapPartnerProviders[key]
	if _, ok := providers[swapAllProviders]; ok {
		return swapReadScope{all: true}, true
	}
	return swapReadScope{providers: providers}, true
}
