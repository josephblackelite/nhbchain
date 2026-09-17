package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/native/lending"
)

const defaultLendingPoolID = "default"

// weiPerWholeUnit is the scaling factor between a wei-denominated amount and
// its whole-token representation (NHB and ZNHB both use 18 decimals).
var weiPerWholeUnit = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

type lendingAccountParams struct {
	Address string `json:"address"`
	PoolID  string `json:"poolId,omitempty"`
}

type lendingMarketResult struct {
	Market         *lending.Market        `json:"market,omitempty"`
	RiskParameters lending.RiskParameters `json:"riskParameters"`
}

type lendingPoolsResult struct {
	Pools          []*lending.Market      `json:"pools"`
	RiskParameters lending.RiskParameters `json:"riskParameters"`
}

// lendingPositionResult describes a single per-pool supply or borrow
// position, matching the shape the finance-hub frontend expects for entries
// in an account's supplied/borrowed arrays.
type lendingPositionResult struct {
	PoolID    string `json:"poolId"`
	AmountWei string `json:"amountWei"`
	ValueUsd  string `json:"valueUsd"`
}

// lendingAccountResult is the JSON-tagged account view returned over RPC.
// lending.UserAccount itself has no JSON tags and carries an unexported
// crypto.Address field that serializes as "{}", so it can never be returned
// to clients directly -- this type is the properly-shaped replacement.
type lendingAccountResult struct {
	Address  string                  `json:"address"`
	Supplied []lendingPositionResult `json:"supplied"`
	Borrowed []lendingPositionResult `json:"borrowed"`
	// CollateralZNHBWei is the raw ZNHB collateral balance, straight from
	// state -- unlike CollateralValueUsd, it needs no oracle price and is
	// never "" when the oracle is unavailable. This is the figure a
	// withdraw-collateral UI should use to know how much a user can
	// actually withdraw.
	CollateralZNHBWei  string `json:"collateralZnhbWei"`
	CollateralValueUsd string `json:"collateralValueUsd"`
	BorrowedValueUsd   string `json:"borrowedValueUsd"`
	RewardsWei         string `json:"rewardsWei"`
}

type lendingUserAccountResult struct {
	Account *lendingAccountResult `json:"account"`
}

// lendingFixedTermLoanResult is the JSON-tagged fixed-term loan view
// returned over RPC -- lending.FixedTermLoan itself has no JSON tags and
// carries an unexported crypto.Address field (Borrower), matching
// lendingAccountResult's own reason for existing above.
type lendingFixedTermLoanResult struct {
	LoanID           string `json:"loanId"`
	Borrower         string `json:"borrower"`
	PoolID           string `json:"poolId"`
	TenureDays       uint64 `json:"tenureDays"`
	RateBps          uint64 `json:"rateBps"`
	PrincipalWei     string `json:"principalWei"`
	TotalInterestWei string `json:"totalInterestWei"`
	RepaidWei        string `json:"repaidWei"`
	OutstandingWei   string `json:"outstandingWei"`
	IssuedAtBlock    uint64 `json:"issuedAtBlock"`
	IssuedAtTime     uint64 `json:"issuedAtTime"`
	MaturityTime     uint64 `json:"maturityTime"`
	Status           string `json:"status"`
	// AutoDebitEnabled/NextAutoDebitCycle/TotalAutoDebitCycles/
	// ConsecutiveMissedAutoDebits were added for the auto-debit billing
	// milestone (core/lending_autodebit_settlement.go) -- surfaced so a
	// portal reconcile job can send escalating warnings BEFORE a loan
	// reaches Status "delinquent", not just detect delinquency after the
	// fact.
	AutoDebitEnabled            bool   `json:"autoDebitEnabled"`
	NextAutoDebitCycle          uint32 `json:"nextAutoDebitCycle"`
	TotalAutoDebitCycles        uint32 `json:"totalAutoDebitCycles"`
	ConsecutiveMissedAutoDebits uint32 `json:"consecutiveMissedAutoDebits"`
}

type lendingFixedTermLoanQueryResult struct {
	Loan *lendingFixedTermLoanResult `json:"loan"`
}

func newLendingFixedTermLoanResult(loan *lending.FixedTermLoan) *lendingFixedTermLoanResult {
	if loan == nil {
		return nil
	}
	principal := "0"
	if loan.PrincipalWei != nil {
		principal = loan.PrincipalWei.String()
	}
	interest := "0"
	if loan.TotalInterestWei != nil {
		interest = loan.TotalInterestWei.String()
	}
	repaid := "0"
	if loan.RepaidWei != nil {
		repaid = loan.RepaidWei.String()
	}
	return &lendingFixedTermLoanResult{
		LoanID:                      hex.EncodeToString(loan.LoanID[:]),
		Borrower:                    loan.Borrower.String(),
		PoolID:                      loan.PoolID,
		TenureDays:                  loan.TenureDays,
		RateBps:                     loan.RateBps,
		PrincipalWei:                principal,
		TotalInterestWei:            interest,
		RepaidWei:                   repaid,
		OutstandingWei:              loan.OutstandingWei().String(),
		IssuedAtBlock:               loan.IssuedAtBlock,
		IssuedAtTime:                loan.IssuedAtTime,
		MaturityTime:                loan.MaturityTime,
		Status:                      string(loan.Status),
		AutoDebitEnabled:            loan.AutoDebitEnabled,
		NextAutoDebitCycle:          loan.NextAutoDebitCycle,
		TotalAutoDebitCycles:        lending.TotalAutoDebitCycles(loan.TenureDays),
		ConsecutiveMissedAutoDebits: loan.ConsecutiveMissedAutoDebits,
	}
}

// weiToDecimalString renders a wei-denominated amount (18 decimals) as a
// trimmed base-10 decimal string, e.g. "12.5", mirroring the nhbportal
// client's own fromWei conversion so amounts surfaced over RPC render
// consistently with values the client formats locally. NHB is a $1-pegged
// asset (see README.md), so this is a direct, always-valid USD converter for
// NHB-denominated amounts (debt, supplied balances). It is NOT used for ZNHB
// collateral -- see collateralValueUsd below, which requires a real oracle
// price and must never fall back to treating ZNHB as 1:1 with NHB (that
// exact fallback caused a live collateral-mispricing incident, 2026-08-24).
func weiToDecimalString(amount *big.Int) string {
	if amount == nil || amount.Sign() == 0 {
		return "0"
	}
	negative := amount.Sign() < 0
	abs := new(big.Int).Abs(amount)
	whole := new(big.Int).Quo(abs, weiPerWholeUnit)
	frac := new(big.Int).Mod(abs, weiPerWholeUnit)
	fracStr := frac.String()
	if len(fracStr) < 18 {
		fracStr = strings.Repeat("0", 18-len(fracStr)) + fracStr
	}
	fracStr = strings.TrimRight(fracStr, "0")
	result := whole.String()
	if fracStr != "" {
		result += "." + fracStr
	}
	if negative {
		result = "-" + result
	}
	return result
}

// collateralValueUsd renders ZNHB collateral's USD value using the SAME
// oracle-adjusted conversion (lending.OracleAdjustedCollateralValue) the
// consensus engine enforces borrows against -- never an independent guess.
// Returns "" when the market has no live reference price yet
// (Market.OracleMedianWei unset/zero), so a caller can render "price
// unavailable" instead of a fabricated dollar figure; it deliberately does
// NOT fall back to weiToDecimalString's 1:1 treatment the way the old code
// here used to.
func collateralValueUsd(market *lending.Market, collateralZNHBWei *big.Int) string {
	if market == nil || market.OracleMedianWei == nil || market.OracleMedianWei.Sign() <= 0 {
		return ""
	}
	return weiToDecimalString(lending.OracleAdjustedCollateralValue(market, collateralZNHBWei))
}

// newLendingAccountResult builds the properly JSON-tagged account view from
// the raw engine state. supplyIndex is the owning market's current supply
// index (or nil if unknown) and is only used to convert the account's LP
// share balance into a redeemable NHB amount, matching the conversion the
// engine already applies internally during Withdraw (see
// lending.RedeemableSupply). market is the same snapshot the caller already
// loaded for supplyIndex, reused here so collateralValueUsd can read its
// oracle price -- nil is fine (renders as no-price-available, not a
// fabricated value).
// fixedTermLoan, when the borrower has one active in this pool, folds its
// OutstandingWei() (principal + locked-in interest, less repayments) into
// BorrowedValueUsd -- otherwise this figure (which the portal uses for its
// health-factor/LTV/max-borrowable display) would silently understate real
// exposure once a fixed-term loan exists, since UserAccount.DebtNHB only
// ever reflects the flexible-rate ledger (see native/lending/engine.go's
// combinedDebtWei for the borrow/withdraw-time enforcement counterpart of
// this same fix). Deliberately NOT added as a second entry in the Borrowed
// position array: that's keyed by poolID and the portal groups positions by
// it, so a second same-poolID entry would just overwrite the flexible one
// rather than add to it -- fixed-term loan detail is already exposed
// separately via lending_getFixedTermLoan, which the portal already reads.
func newLendingAccountResult(poolID string, addr [20]byte, account *lending.UserAccount, supplyIndex *big.Int, market *lending.Market, fixedTermLoan *lending.FixedTermLoan) *lendingAccountResult {
	result := &lendingAccountResult{
		Address:  "0x" + hex.EncodeToString(addr[:]),
		Supplied: []lendingPositionResult{},
		Borrowed: []lendingPositionResult{},
		// The lending engine does not yet accrue a separate rewards balance
		// (see native/lending/engine.go); report zero rather than inventing
		// a figure until that mechanism exists.
		RewardsWei:         "0",
		CollateralZNHBWei:  "0",
		CollateralValueUsd: "0",
		BorrowedValueUsd:   "0",
	}
	if account == nil {
		return result
	}

	if account.SupplyShares != nil && account.SupplyShares.Sign() > 0 {
		redeemable := lending.RedeemableSupply(account.SupplyShares, supplyIndex)
		result.Supplied = append(result.Supplied, lendingPositionResult{
			PoolID:    poolID,
			AmountWei: redeemable.String(),
			ValueUsd:  weiToDecimalString(redeemable),
		})
	}

	if account.DebtNHB != nil && account.DebtNHB.Sign() > 0 {
		result.Borrowed = append(result.Borrowed, lendingPositionResult{
			PoolID:    poolID,
			AmountWei: account.DebtNHB.String(),
			ValueUsd:  weiToDecimalString(account.DebtNHB),
		})
	}

	if account.CollateralZNHB != nil {
		result.CollateralZNHBWei = account.CollateralZNHB.String()
	}
	result.CollateralValueUsd = collateralValueUsd(market, account.CollateralZNHB)

	combinedBorrowedWei := big.NewInt(0)
	if account.DebtNHB != nil {
		combinedBorrowedWei = combinedBorrowedWei.Add(combinedBorrowedWei, account.DebtNHB)
	}
	if fixedTermLoan != nil {
		combinedBorrowedWei = combinedBorrowedWei.Add(combinedBorrowedWei, fixedTermLoan.OutstandingWei())
	}
	result.BorrowedValueUsd = weiToDecimalString(combinedBorrowedWei)
	return result
}

func (s *Server) handleLendingGetMarket(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	var poolID string
	if len(req.Params) == 1 {
		var raw interface{}
		if err := json.Unmarshal(req.Params[0], &raw); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter", err.Error())
			return
		}
		switch value := raw.(type) {
		case string:
			poolID = value
		case map[string]interface{}:
			if v, ok := value["poolId"].(string); ok {
				poolID = v
			}
		default:
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "unsupported parameter", nil)
			return
		}
	} else if len(req.Params) > 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "too many parameters", nil)
		return
	}
	market, params, moduleErr := s.lending.GetMarket(poolID)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	result := lendingMarketResult{RiskParameters: params}
	if market != nil {
		result.Market = market
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleLendGetPools(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	pools, params, moduleErr := s.lending.GetPools()
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	if pools == nil {
		pools = []*lending.Market{}
	}
	writeResult(w, req.ID, lendingPoolsResult{Pools: pools, RiskParameters: params})
}

// lending_createPool (and LendingModule.CreatePool, rpc/modules/lending.go)
// were removed -- the old handler wrote a brand new market straight into
// the live pending state trie via Node.WithState (a direct write invisible
// to every other validator, guaranteed to diverge state roots the moment
// more than one validator exists) and trusted a client-supplied
// developerOwner address with zero proof of key possession. Pool creation
// is now a real signed transaction (TxTypeLendingCreatePool,
// core/lending_native.go's applyLendingCreatePoolTransaction), submitted
// via nhb_sendTransaction like every other signed native transaction type.
// Confirmed via full-repo grep before removal: lending_createPool had no
// callers anywhere in this repo or nhbportal.

func (s *Server) handleLendingGetUserAccount(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address parameter", nil)
		return
	}
	var addressParam string
	poolID := defaultLendingPoolID
	if err := json.Unmarshal(req.Params[0], &addressParam); err != nil {
		var wrapped lendingAccountParams
		if err := json.Unmarshal(req.Params[0], &wrapped); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", err.Error())
			return
		}
		addressParam = wrapped.Address
		if strings.TrimSpace(wrapped.PoolID) != "" {
			poolID = wrapped.PoolID
		}
	}
	trimmed := strings.TrimSpace(addressParam)
	if trimmed == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address required", nil)
		return
	}
	addr, err := decodeBech32(trimmed)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	account, moduleErr := s.lending.GetUserAccount(poolID, addr)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	if account == nil {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "account not found", trimmed)
		return
	}

	resolvedPoolID := strings.TrimSpace(poolID)
	if resolvedPoolID == "" {
		resolvedPoolID = defaultLendingPoolID
	}

	// The market snapshot is only consulted to convert the account's LP
	// share balance into a redeemable NHB amount via the current supply
	// index; a missing/uninitialised market is not an error here, it just
	// means there is nothing to redeem yet.
	market, _, marketErr := s.lending.GetMarket(resolvedPoolID)
	if marketErr != nil {
		writeError(w, marketErr.HTTPStatus, req.ID, marketErr.Code, marketErr.Message, marketErr.Data)
		return
	}
	var supplyIndex *big.Int
	if market != nil {
		supplyIndex = market.SupplyIndex
		// GetMarket above already projects live interest accrual into
		// market.BorrowIndex (see LendingModule.projectMarketAccrual), but
		// account.DebtNHB itself was loaded before that projection existed
		// and never gets re-derived from it on its own -- ProjectUserDebt
		// re-syncs it, same math the engine runs before every real
		// Borrow/Repay/Liquidate. A bare zero-value Engine is correct here:
		// this method never reads its receiver.
		(&lending.Engine{}).ProjectUserDebt(account, market)
	}

	// Folded into BorrowedValueUsd (see newLendingAccountResult) so the
	// portal's health-factor/LTV/max-borrowable figures reflect a fixed-term
	// loan's outstanding claim too, not just the flexible-rate ledger.
	fixedTermLoan, fixedTermErr := s.lending.GetActiveFixedTermLoan(resolvedPoolID, addr)
	if fixedTermErr != nil {
		writeError(w, fixedTermErr.HTTPStatus, req.ID, fixedTermErr.Code, fixedTermErr.Message, fixedTermErr.Data)
		return
	}

	writeResult(w, req.ID, lendingUserAccountResult{
		Account: newLendingAccountResult(resolvedPoolID, addr, account, supplyIndex, market, fixedTermLoan),
	})
}

// handleLendingGetFixedTermLoan returns the queried address's currently
// active fixed-term loan in the pool, or {"loan": null} if they have none --
// not an error, since most addresses have no active fixed-term loan at any
// given time.
func (s *Server) handleLendingGetFixedTermLoan(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address parameter", nil)
		return
	}
	var addressParam string
	poolID := defaultLendingPoolID
	if err := json.Unmarshal(req.Params[0], &addressParam); err != nil {
		var wrapped lendingAccountParams
		if err := json.Unmarshal(req.Params[0], &wrapped); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", err.Error())
			return
		}
		addressParam = wrapped.Address
		if strings.TrimSpace(wrapped.PoolID) != "" {
			poolID = wrapped.PoolID
		}
	}
	trimmed := strings.TrimSpace(addressParam)
	if trimmed == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address required", nil)
		return
	}
	addr, err := decodeBech32(trimmed)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	loan, moduleErr := s.lending.GetActiveFixedTermLoan(poolID, addr)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	writeResult(w, req.ID, lendingFixedTermLoanQueryResult{
		Loan: newLendingFixedTermLoanResult(loan),
	})
}

// lendingFixedTermDepositResult is the JSON-tagged fixed-term deposit view
// returned over RPC -- lending.FixedTermDeposit itself has no JSON tags and
// carries an unexported crypto.Address field (Depositor), matching
// lendingFixedTermLoanResult's own reason for existing.
type lendingFixedTermDepositResult struct {
	DepositID              string `json:"depositId"`
	Depositor              string `json:"depositor"`
	PoolID                 string `json:"poolId"`
	TenureDays             uint64 `json:"tenureDays"`
	RateBps                uint64 `json:"rateBps"`
	PrincipalWei           string `json:"principalWei"`
	TotalInterestOwedWei   string `json:"totalInterestOwedWei"`
	PaidInterestWei        string `json:"paidInterestWei"`
	OutstandingInterestWei string `json:"outstandingInterestWei"`
	Payout                 string `json:"payout"`
	IssuedAtBlock          uint64 `json:"issuedAtBlock"`
	IssuedAtTime           uint64 `json:"issuedAtTime"`
	MaturityTime           uint64 `json:"maturityTime"`
	Status                 string `json:"status"`
	NextPayoutCycle        uint32 `json:"nextPayoutCycle"`
}

type lendingFixedTermDepositQueryResult struct {
	Deposit *lendingFixedTermDepositResult `json:"deposit"`
}

func newLendingFixedTermDepositResult(deposit *lending.FixedTermDeposit) *lendingFixedTermDepositResult {
	if deposit == nil {
		return nil
	}
	principal := "0"
	if deposit.PrincipalWei != nil {
		principal = deposit.PrincipalWei.String()
	}
	interestOwed := "0"
	if deposit.TotalInterestOwedWei != nil {
		interestOwed = deposit.TotalInterestOwedWei.String()
	}
	paidInterest := "0"
	if deposit.PaidInterestWei != nil {
		paidInterest = deposit.PaidInterestWei.String()
	}
	return &lendingFixedTermDepositResult{
		DepositID:              hex.EncodeToString(deposit.DepositID[:]),
		Depositor:              deposit.Depositor.String(),
		PoolID:                 deposit.PoolID,
		TenureDays:             deposit.TenureDays,
		RateBps:                deposit.RateBps,
		PrincipalWei:           principal,
		TotalInterestOwedWei:   interestOwed,
		PaidInterestWei:        paidInterest,
		OutstandingInterestWei: deposit.OutstandingInterestWei().String(),
		Payout:                 string(deposit.Payout),
		IssuedAtBlock:          deposit.IssuedAtBlock,
		IssuedAtTime:           deposit.IssuedAtTime,
		MaturityTime:           deposit.MaturityTime,
		Status:                 string(deposit.Status),
		NextPayoutCycle:        deposit.NextPayoutCycle,
	}
}

// handleLendingGetFixedTermDeposit returns the fixed-term deposit with the
// given ID, or {"deposit": null} if it does not exist -- not an error.
// Looked up directly by depositId (unlike lending_getFixedTermLoan's
// address-based lookup): a depositor may hold multiple simultaneous
// fixed-term deposits, so there is no single "the active one" to resolve
// from an address alone.
func (s *Server) handleLendingGetFixedTermDeposit(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected depositId parameter", nil)
		return
	}
	var depositIDParam string
	if err := json.Unmarshal(req.Params[0], &depositIDParam); err != nil {
		var wrapped struct {
			DepositID string `json:"depositId"`
		}
		if err := json.Unmarshal(req.Params[0], &wrapped); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid depositId parameter", err.Error())
			return
		}
		depositIDParam = wrapped.DepositID
	}
	normalized := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(depositIDParam), "0x"))
	if normalized == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "depositId required", nil)
		return
	}
	raw, err := hex.DecodeString(normalized)
	if err != nil || len(raw) != 32 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid depositId", "expected a 32-byte hex string")
		return
	}
	var depositID [32]byte
	copy(depositID[:], raw)

	deposit, moduleErr := s.lending.GetFixedTermDeposit(depositID)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	writeResult(w, req.ID, lendingFixedTermDepositQueryResult{
		Deposit: newLendingFixedTermDepositResult(deposit),
	})
}

// handleLendingSupplyNHB, handleLendingWithdrawNHB, handleLendingDepositZNHB,
// handleLendingWithdrawZNHB, handleLendingBorrowNHB,
// handleLendingBorrowNHBWithFee, handleLendingRepayNHB, and
// handleLendingLiquidate are deliberately disabled -- see docs/issue30.md
// item 24. They mutated a client-supplied address's lending position
// directly via WithState, with no signature proving the caller controls
// that address -- gated only by the shared admin JWT, so anyone holding it
// could supply/withdraw/borrow/repay/liquidate on behalf of any address.
// nhbportal never used these (it signs real TxTypeLendingSupplyNHB /
// TxTypeLendingWithdrawNHB / TxTypeLendingDepositZNHB /
// TxTypeLendingWithdrawZNHB / TxTypeLendingBorrowNHB / TxTypeLendingRepayNHB
// transactions instead, which core/state_transition.go already applies
// correctly), so nothing legitimate depends on them. Liquidate now has a
// real signed-transaction equivalent too (TxTypeLendingLiquidate, added for
// issue30 item 25) -- unlike the other six, it's signed by the liquidator,
// not the borrower, since liquidation is inherently a permissionless
// third-party action against someone else's unhealthy position. Fail loudly
// rather than silently accept an unauthenticated instruction to move
// someone else's funds.
const lendingRPCDisabledMessage = "this method is disabled; sign a transaction (TxTypeLendingSupplyNHB/TxTypeLendingWithdrawNHB/TxTypeLendingDepositZNHB/TxTypeLendingWithdrawZNHB/TxTypeLendingBorrowNHB/TxTypeLendingRepayNHB/TxTypeLendingLiquidate) via nhb_sendTransaction instead, so the caller's own signature authorizes the action"

func (s *Server) handleLendingSupplyNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, lendingRPCDisabledMessage, nil)
}

func (s *Server) handleLendingWithdrawNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, lendingRPCDisabledMessage, nil)
}

func (s *Server) handleLendingDepositZNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, lendingRPCDisabledMessage, nil)
}

func (s *Server) handleLendingWithdrawZNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, lendingRPCDisabledMessage, nil)
}

func (s *Server) handleLendingBorrowNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, lendingRPCDisabledMessage, nil)
}

func (s *Server) handleLendingBorrowNHBWithFee(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, lendingRPCDisabledMessage, nil)
}

func (s *Server) handleLendingRepayNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, lendingRPCDisabledMessage, nil)
}

func (s *Server) handleLendingLiquidate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, lendingRPCDisabledMessage, nil)
}
