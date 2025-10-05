package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type potsoStakeLockParams struct {
        Owner     string `json:"owner"`
        Amount    string `json:"amount"`
        Nonce     uint64 `json:"nonce"`
        Signature string `json:"signature"`
}

type potsoStakeUnbondParams struct {
        Owner     string `json:"owner"`
        Amount    string `json:"amount"`
        Nonce     uint64 `json:"nonce"`
        Signature string `json:"signature"`
}

type potsoStakeWithdrawParams struct {
        Owner     string `json:"owner"`
        Nonce     uint64 `json:"nonce"`
        Signature string `json:"signature"`
}

type potsoStakeInfoParams struct {
	Owner string `json:"owner"`
}

type potsoStakeLockResult struct {
	OK    bool   `json:"ok"`
	Nonce uint64 `json:"nonce"`
}

type potsoStakeUnbondResult struct {
	OK         bool   `json:"ok"`
	Amount     string `json:"amount"`
	WithdrawAt uint64 `json:"withdrawAt"`
}

type potsoStakeWithdrawResult struct {
	Withdrawn []potsoStakeWithdrawEntry `json:"withdrawn"`
}

type potsoStakeWithdrawEntry struct {
	Nonce  uint64 `json:"nonce"`
	Amount string `json:"amount"`
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

func potsoStakeDigest(action, owner string, amount *big.Int, nonce uint64) []byte {
        normalizedOwner := strings.ToLower(strings.TrimSpace(owner))
        payload := "potso_stake_" + action + "|" + normalizedOwner
        if amount != nil {
                payload += "|" + amount.String()
        }
        payload += "|" + fmt.Sprint(nonce)
        digest := sha256.Sum256([]byte(payload))
        return digest[:]
}

func decodeStakeSignature(action, owner string, amount *big.Int, nonce uint64, signature string) ([20]byte, error) {
        var zero [20]byte
        if nonce == 0 {
                return zero, fmt.Errorf("nonce must be greater than zero")
        }
        digest := potsoStakeDigest(action, owner, amount, nonce)
        sigBytes, err := decodeHexBytes(signature)
        if err != nil {
                return zero, err
        }
        if len(sigBytes) != 65 {
		return zero, fmt.Errorf("signature must be 65 bytes")
	}
	pubKey, err := ethcrypto.SigToPub(digest, sigBytes)
	if err != nil {
		return zero, fmt.Errorf("invalid signature: %w", err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	ownerAddr, err := decodeBech32(owner)
	if err != nil {
		return zero, err
	}
	if !strings.EqualFold(recovered.Hex()[2:], hex.EncodeToString(ownerAddr[:])) {
		return zero, fmt.Errorf("signature does not match owner")
	}
	return ownerAddr, nil
}

func amountFromString(value string) (*big.Int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, fmt.Errorf("amount required")
	}
	amt := new(big.Int)
	if _, ok := amt.SetString(trimmed, 10); !ok {
		return nil, fmt.Errorf("invalid amount")
	}
	if amt.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return amt, nil
}

func (s *Server) handlePotsoStakeLock(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
        if len(req.Params) != 1 {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
                return
        }
        var params potsoStakeLockParams
        if err := json.Unmarshal(req.Params[0], &params); err != nil {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
                return
        }
        amount, err := amountFromString(params.Amount)
        if err != nil {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
                return
        }
        ownerAddr, err := decodeStakeSignature("lock", params.Owner, amount, params.Nonce, params.Signature)
        if err != nil {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
                return
        }
        nonce, _, err := s.node.PotsoStakeLock(ownerAddr, amount, params.Nonce)
        if err != nil {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
                return
        }
        writeResult(w, req.ID, potsoStakeLockResult{OK: true, Nonce: nonce})
}

func (s *Server) handlePotsoStakeUnbond(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
        if len(req.Params) != 1 {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
                return
        }
	var params potsoStakeUnbondParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
        amount, err := amountFromString(params.Amount)
        if err != nil {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
                return
        }
        ownerAddr, err := decodeStakeSignature("unbond", params.Owner, amount, params.Nonce, params.Signature)
        if err != nil {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
                return
        }
        total, withdrawAt, err := s.node.PotsoStakeUnbond(ownerAddr, amount, params.Nonce)
        if err != nil {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
                return
        }
        writeResult(w, req.ID, potsoStakeUnbondResult{OK: true, Amount: total.String(), WithdrawAt: withdrawAt})
}

func (s *Server) handlePotsoStakeWithdraw(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
        if len(req.Params) != 1 {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
                return
        }
	var params potsoStakeWithdrawParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
        ownerAddr, err := decodeStakeSignature("withdraw", params.Owner, nil, params.Nonce, params.Signature)
        if err != nil {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
                return
        }
        payouts, err := s.node.PotsoStakeWithdraw(ownerAddr, params.Nonce)
        if err != nil {
                writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
                return
        }
	result := potsoStakeWithdrawResult{Withdrawn: make([]potsoStakeWithdrawEntry, len(payouts))}
	for i, payout := range payouts {
		amount := "0"
		if payout.Amount != nil {
			amount = payout.Amount.String()
		}
		result.Withdrawn[i] = potsoStakeWithdrawEntry{Nonce: payout.Nonce, Amount: amount}
	}
	writeResult(w, req.ID, result)
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
