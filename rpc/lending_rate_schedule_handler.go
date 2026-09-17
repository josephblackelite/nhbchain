package rpc

import (
	"net/http"
	"sort"
)

// handleLendingGetRateSchedule returns the currently-effective fixed-term
// tenure->rate table: the governance param store's value if a
// policy.lendingRateSchedule proposal has ever executed, otherwise the
// conservative built-in default -- see
// native/lending.DefaultFixedTermRateSchedule, read fresh from state on
// every call (core/node.go's LendingFixedTermRateSchedule). Mirrors
// handleSwapGetRiskParams's precedent exactly: network-wide only, no
// account-specific data, so any caller -- including a governance UI
// drafting a proposal -- can see the current values. Deliberately ungated,
// matching every other lending_get* read method (lending_getMarket,
// lending_getUserAccount, lending_getFixedTermLoan) -- none of them require
// requireAuthInto.
//
// The response shape -- {"schedule":[{"tenureDays":...,"rateBps":...},...]}
// -- deliberately mirrors governance.LendingRateSchedulePayload's own JSON
// shape exactly, so a governance UI can round-trip this response straight
// into a policy.lendingRateSchedule proposal payload without reshaping it.
// Entries are sorted by tenureDays for a stable, deterministic response
// (TenureRateSchedule is a Go map, whose iteration order is not stable).
func (s *Server) handleLendingGetRateSchedule(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	schedule, err := s.node.LendingFixedTermRateSchedule()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load lending rate schedule", err.Error())
		return
	}
	tenures := make([]uint64, 0, len(schedule))
	for tenureDays := range schedule {
		tenures = append(tenures, tenureDays)
	}
	sort.Slice(tenures, func(i, j int) bool { return tenures[i] < tenures[j] })
	entries := make([]map[string]interface{}, 0, len(tenures))
	for _, tenureDays := range tenures {
		entries = append(entries, map[string]interface{}{
			"tenureDays": tenureDays,
			"rateBps":    schedule[tenureDays],
		})
	}
	result := map[string]interface{}{
		"schedule": entries,
	}
	writeResult(w, req.ID, result)
}
