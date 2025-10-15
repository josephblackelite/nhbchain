package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/native/lending"
	"nhbchain/rpc/modules"
)

const defaultLendingPoolID = "default"

type lendingAccountParams struct {
	Address string `json:"address"`
	PoolID  string `json:"poolId,omitempty"`
}

type lendingAmountParams struct {
	From   string `json:"from"`
	Amount string `json:"amount"`
	PoolID string `json:"poolId,omitempty"`
}

type lendingBorrowParams struct {
	Borrower string `json:"borrower"`
	Amount   string `json:"amount"`
	PoolID   string `json:"poolId,omitempty"`
}

type lendingBorrowWithFeeParams struct {
	Borrower string `json:"borrower"`
	Amount   string `json:"amount"`
	PoolID   string `json:"poolId,omitempty"`
}

type lendingLiquidateParams struct {
	Liquidator string `json:"liquidator"`
	Borrower   string `json:"borrower"`
	PoolID     string `json:"poolId,omitempty"`
}

type lendingTxResult struct {
	TxHash string `json:"txHash"`
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

func (s *Server) handleLendingSupplyNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	s.handleLendingAmountTx(w, r, req, s.lending.SupplyNHB)
}

func (s *Server) handleLendingWithdrawNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	s.handleLendingAmountTx(w, r, req, s.lending.WithdrawNHB)
}

func (s *Server) handleLendingDepositZNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	s.handleLendingAmountTx(w, r, req, s.lending.DepositZNHB)
}

func (s *Server) handleLendingWithdrawZNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	s.handleLendingAmountTx(w, r, req, s.lending.WithdrawZNHB)
}

func (s *Server) handleLendingBorrowNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	poolID, addr, amount, ok := s.parseBorrowParams(w, req)
	if !ok {
		return
	}
	txHash, moduleErr := s.lending.BorrowNHB(poolID, addr, amount)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	writeResult(w, req.ID, lendingTxResult{TxHash: txHash})
}

func (s *Server) handleLendingBorrowNHBWithFee(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected parameter object", nil)
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(req.Params[0], &raw); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	if _, ok := raw["feeRecipient"]; ok {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "feeRecipient is not configurable", nil)
		return
	}
	if _, ok := raw["feeBps"]; ok {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "feeBps is not configurable", nil)
		return
	}
	var params lendingBorrowWithFeeParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	borrower, err := decodeBech32(params.Borrower)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid borrower", err.Error())
		return
	}
	amount, err := parseAmount(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	txHash, moduleErr := s.lending.BorrowNHBWithFee(params.PoolID, borrower, amount)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	writeResult(w, req.ID, lendingTxResult{TxHash: txHash})
}

func (s *Server) handleLendingRepayNHB(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	s.handleLendingAmountTx(w, r, req, s.lending.RepayNHB)
}

func (s *Server) handleLendingLiquidate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected parameter object", nil)
		return
	}
	var params lendingLiquidateParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	liquidator, err := decodeBech32(params.Liquidator)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid liquidator", err.Error())
		return
	}
	borrower, err := decodeBech32(params.Borrower)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid borrower", err.Error())
		return
	}
	txHash, moduleErr := s.lending.Liquidate(params.PoolID, liquidator, borrower)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	writeResult(w, req.ID, lendingTxResult{TxHash: txHash})
}

func (s *Server) handleLendingAmountTx(w http.ResponseWriter, r *http.Request, req *RPCRequest, fn func(string, [20]byte, *big.Int) (string, *modules.ModuleError)) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected parameter object", nil)
		return
	}
	var params lendingAmountParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	addr, err := decodeBech32(params.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid from", err.Error())
		return
	}
	amount, err := parseAmount(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	poolID := strings.TrimSpace(params.PoolID)
	txHash, moduleErr := fn(poolID, addr, amount)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	writeResult(w, req.ID, lendingTxResult{TxHash: txHash})
}

func (s *Server) parseBorrowParams(w http.ResponseWriter, req *RPCRequest) (string, [20]byte, *big.Int, bool) {
	var params lendingBorrowParams
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected parameter object", nil)
		return "", [20]byte{}, nil, false
	}
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return "", [20]byte{}, nil, false
	}
	borrower, err := decodeBech32(params.Borrower)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid borrower", err.Error())
		return "", [20]byte{}, nil, false
	}
	amount, err := parseAmount(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return "", [20]byte{}, nil, false
	}
	return params.PoolID, borrower, amount, true
}
