package rpc

import (
	"encoding/json"
	"net/http"
)

// potso_stake_lock/potso_stake_unbond/potso_stake_withdraw (and their
// bespoke sha256/secp256k1 signature + authNonce scheme, decodeStakeSignature/
// potsoStakeDigest) were removed -- those RPC methods mutated
// n.state.Trie directly via Node.PotsoStakeLock/Unbond/Withdraw
// (core/node.go, also removed), outside CreateBlock/ApplyTransaction
// entirely, the same direct-state-write bug already fixed for governance
// and lending's CreatePool this session. Stake actions are now real signed
// transactions (TxTypePotsoStakeLock/Unbond/Withdraw,
// core/potso_stake_tx.go), submitted via nhb_sendTransaction like every
// other signed native transaction type -- the owner is always the
// transaction's recovered signer, never a payload field, and replay
// protection comes from the standard account nonce, not a separate
// bespoke one. Only the read-only info query remains here.

type potsoStakeInfoParams struct {
	Owner string `json:"owner"`
}

type potsoStakeInfoResult struct {
	Bonded        string               `json:"bonded"`
	PendingUnbond string               `json:"pendingUnbond"`
	Withdrawable  string               `json:"withdrawable"`
	Locks         []potsoStakeInfoLock `json:"locks"`
}

type potsoStakeInfoLock struct {
	Nonce      uint64 `json:"nonce"`
	Amount     string `json:"amount"`
	CreatedAt  uint64 `json:"createdAt"`
	UnbondAt   uint64 `json:"unbondAt"`
	WithdrawAt uint64 `json:"withdrawAt"`
}

func (s *Server) handlePotsoStakeInfo(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	var params potsoStakeInfoParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	ownerAddr, err := decodeBech32(params.Owner)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid owner", err.Error())
		return
	}
	info, err := s.node.PotsoStakeInfo(ownerAddr)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	result := potsoStakeInfoResult{
		Bonded:        info.Bonded.String(),
		PendingUnbond: info.PendingUnbond.String(),
		Withdrawable:  info.Withdrawable.String(),
		Locks:         make([]potsoStakeInfoLock, len(info.Locks)),
	}
	for i, lock := range info.Locks {
		amount := "0"
		if lock.Amount != nil {
			amount = lock.Amount.String()
		}
		result.Locks[i] = potsoStakeInfoLock{
			Nonce:      lock.Nonce,
			Amount:     amount,
			CreatedAt:  lock.CreatedAt,
			UnbondAt:   lock.UnbondAt,
			WithdrawAt: lock.WithdrawAt,
		}
	}
	writeResult(w, req.ID, result)
}
