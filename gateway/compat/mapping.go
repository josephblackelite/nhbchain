package compat

import "net/http"

var DefaultMappings = map[string]Mapping{
	"lending_getMarket":        {Service: "lendingd", Path: "/v1/lending/markets/get", Method: http.MethodPost},
	"lend_getPools":            {Service: "lendingd", Path: "/v1/lending/pools", Method: http.MethodGet},
	"lend_createPool":          {Service: "lendingd", Path: "/v1/lending/pools", Method: http.MethodPost},
	"lending_getUserAccount":   {Service: "lendingd", Path: "/v1/lending/accounts/get", Method: http.MethodPost},
	"lending_supplyNHB":        {Service: "lendingd", Path: "/v1/lending/supply", Method: http.MethodPost},
	"lending_withdrawNHB":      {Service: "lendingd", Path: "/v1/lending/withdraw", Method: http.MethodPost},
	"lending_depositZNHB":      {Service: "lendingd", Path: "/v1/lending/collateral/deposit", Method: http.MethodPost},
	"lending_withdrawZNHB":     {Service: "lendingd", Path: "/v1/lending/collateral/withdraw", Method: http.MethodPost},
	"lending_borrowNHB":        {Service: "lendingd", Path: "/v1/lending/borrow", Method: http.MethodPost},
	"lending_borrowNHBWithFee": {Service: "lendingd", Path: "/v1/lending/borrow/with-fee", Method: http.MethodPost},
	"lending_repayNHB":         {Service: "lendingd", Path: "/v1/lending/repay", Method: http.MethodPost},
	"lending_liquidate":        {Service: "lendingd", Path: "/v1/lending/liquidate", Method: http.MethodPost},

	"swap_submitVoucher":   {Service: "swapd", Path: "/v1/swap/voucher/submit", Method: http.MethodPost},
	"swap_voucher_get":     {Service: "swapd", Path: "/v1/swap/voucher/get", Method: http.MethodPost},
	"swap_voucher_list":    {Service: "swapd", Path: "/v1/swap/voucher/list", Method: http.MethodPost},
	"swap_voucher_export":  {Service: "swapd", Path: "/v1/swap/voucher/export", Method: http.MethodPost},
	"swap_limits":          {Service: "swapd", Path: "/v1/swap/limits", Method: http.MethodGet},
	"swap_provider_status": {Service: "swapd", Path: "/v1/swap/providers/status", Method: http.MethodGet},
	"swap_burn_list":       {Service: "swapd", Path: "/v1/swap/burn/list", Method: http.MethodGet},
	"swap_voucher_reverse": {Service: "swapd", Path: "/v1/swap/voucher/reverse", Method: http.MethodPost},

	"gov_getProposal":    {Service: "governd", Path: "/v1/gov/proposals/get", Method: http.MethodPost},
	"gov_listProposals":  {Service: "governd", Path: "/v1/gov/proposals", Method: http.MethodGet},
	"gov_getTally":       {Service: "governd", Path: "/v1/gov/proposals/tally", Method: http.MethodPost},
	"gov_submitProposal": {Service: "governd", Path: "/v1/gov/proposals", Method: http.MethodPost},
	"gov_vote":           {Service: "governd", Path: "/v1/gov/votes", Method: http.MethodPost},
	"gov_deposit":        {Service: "governd", Path: "/v1/gov/deposits", Method: http.MethodPost},

	"consensus_status":     {Service: "consensusd", Path: "/v1/consensus/status", Method: http.MethodGet},
	"consensus_validators": {Service: "consensusd", Path: "/v1/consensus/validators", Method: http.MethodGet},
	"consensus_block":      {Service: "consensusd", Path: "/v1/consensus/block", Method: http.MethodPost},
}
