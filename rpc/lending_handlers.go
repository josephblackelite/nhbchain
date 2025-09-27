package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/native/lending"
	"nhbchain/rpc/modules"
)

type lendingAccountParams struct {
	Address string `json:"address"`
}

type lendingAmountParams struct {
	From   string `json:"from"`
	Amount string `json:"amount"`
}

type lendingBorrowParams struct {
	Borrower string `json:"borrower"`
	Amount   string `json:"amount"`
}

type lendingBorrowWithFeeParams struct {
	Borrower     string `json:"borrower"`
	Amount       string `json:"amount"`
	FeeRecipient string `json:"feeRecipient"`
	FeeBps       uint64 `json:"feeBps"`
}

type lendingLiquidateParams struct {
	Liquidator string `json:"liquidator"`
	Borrower   string `json:"borrower"`
}

type lendingTxResult struct {
	TxHash string `json:"txHash"`
}

type lendingMarketResult struct {
	Market         *lending.Market        `json:"market,omitempty"`
	RiskParameters lending.RiskParameters `json:"riskParameters"`
}

type lendingUserAccountResult struct {
	Account *lending.UserAccount `json:"account"`
}

func (s *Server) handleLendingGetMarket(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	market, params, moduleErr := s.lending.GetMarket()
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

func (s *Server) handleLendingGetUserAccount(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address parameter", nil)
		return
	}
	var addressParam string
	if err := json.Unmarshal(req.Params[0], &addressParam); err != nil {
		var wrapped lendingAccountParams
		if err := json.Unmarshal(req.Params[0], &wrapped); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", err.Error())
			return
		}
		addressParam = wrapped.Address
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
	account, moduleErr := s.lending.GetUserAccount(addr)
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
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	addr, amount, ok := s.parseBorrowParams(w, req)
	if !ok {
		return
	}
	txHash, moduleErr := s.lending.BorrowNHB(addr, amount)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	writeResult(w, req.ID, lendingTxResult{TxHash: txHash})
}

func (s *Server) handleLendingBorrowNHBWithFee(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected parameter object", nil)
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
	recipient, err := decodeBech32(params.FeeRecipient)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid feeRecipient", err.Error())
		return
	}
	txHash, moduleErr := s.lending.BorrowNHBWithFee(borrower, amount, recipient, params.FeeBps)
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
	if authErr := s.requireAuth(r); authErr != nil {
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
	txHash, moduleErr := s.lending.Liquidate(liquidator, borrower)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	writeResult(w, req.ID, lendingTxResult{TxHash: txHash})
}

func (s *Server) handleLendingAmountTx(w http.ResponseWriter, r *http.Request, req *RPCRequest, fn func([20]byte, *big.Int) (string, *modules.ModuleError)) {
	if authErr := s.requireAuth(r); authErr != nil {
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
	txHash, moduleErr := fn(addr, amount)
	if moduleErr != nil {
		writeError(w, moduleErr.HTTPStatus, req.ID, moduleErr.Code, moduleErr.Message, moduleErr.Data)
		return
	}
	writeResult(w, req.ID, lendingTxResult{TxHash: txHash})
}

func (s *Server) parseBorrowParams(w http.ResponseWriter, req *RPCRequest) ([20]byte, *big.Int, bool) {
	var params lendingBorrowParams
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected parameter object", nil)
		return [20]byte{}, nil, false
	}
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return [20]byte{}, nil, false
	}
	borrower, err := decodeBech32(params.Borrower)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid borrower", err.Error())
		return [20]byte{}, nil, false
	}
	amount, err := parseAmount(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return [20]byte{}, nil, false
	}
	return borrower, amount, true
}
