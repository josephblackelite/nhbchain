package rpc

import (
	"encoding/json"
	"net/http"
	"strings"

	"nhbchain/native/lending"
)

const defaultLendingPoolID = "default"

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

type lendingUserAccountResult struct {
	Account *lending.UserAccount `json:"account"`
}

type lendingCreatePoolParams struct {
	PoolID         string `json:"poolId"`
	DeveloperOwner string `json:"developerOwner"`
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

func (s *Server) handleLendCreatePool(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected parameter object", nil)
		return
	}
	var input lendingCreatePoolParams
	if err := json.Unmarshal(req.Params[0], &input); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	poolID := strings.TrimSpace(input.PoolID)
	if poolID == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "poolId required", nil)
		return
	}
	ownerAddr, err := decodeBech32(input.DeveloperOwner)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid developerOwner", err.Error())
		return
	}
	market, moduleErr := s.lending.CreatePool(poolID, ownerAddr)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	_, risk, paramsErr := s.lending.GetMarket(poolID)
	if paramsErr != nil {
		writeError(w, paramsErr.HTTPStatus, req.ID, paramsErr.Code, paramsErr.Message, paramsErr.Data)
		return
	}
	result := lendingMarketResult{Market: market, RiskParameters: risk}
	writeResult(w, req.ID, result)
}

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
	writeResult(w, req.ID, lendingUserAccountResult{Account: account})
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
