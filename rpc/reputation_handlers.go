package rpc

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"nhbchain/core"
	"nhbchain/native/reputation"
)

type reputationVerifyParams struct {
	Verifier  string `json:"verifier"`
	Subject   string `json:"subject"`
	Skill     string `json:"skill"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
}

type reputationVerificationJSON struct {
	Verifier  string `json:"verifier"`
	Subject   string `json:"subject"`
	Skill     string `json:"skill"`
	IssuedAt  int64  `json:"issuedAt"`
	ExpiresAt *int64 `json:"expiresAt,omitempty"`
}

func (s *Server) handleReputationVerifySkill(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params reputationVerifyParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", err.Error())
		return
	}
	verifier, err := parseBech32Address(params.Verifier)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", err.Error())
		return
	}
	subject, err := parseBech32Address(params.Subject)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", err.Error())
		return
	}
	skill := strings.TrimSpace(params.Skill)
	if skill == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", "skill required")
		return
	}
	var expires int64
	if params.ExpiresAt > 0 {
		expires = params.ExpiresAt
	}
	verification, err := s.node.ReputationVerifySkill(verifier, subject, skill, expires)
	if err != nil {
		writeReputationError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, formatReputationVerificationJSON(verification))
}

func formatReputationVerificationJSON(v *reputation.SkillVerification) reputationVerificationJSON {
	if v == nil {
		return reputationVerificationJSON{}
	}
	var expiresPtr *int64
	if v.ExpiresAt > 0 {
		expires := v.ExpiresAt
		expiresPtr = &expires
	}
	issued := v.IssuedAt
	if issued == 0 {
		issued = time.Now().Unix()
	}
	return reputationVerificationJSON{
		Verifier:  formatAddress(v.Verifier),
		Subject:   formatAddress(v.Subject),
		Skill:     v.Skill,
		IssuedAt:  issued,
		ExpiresAt: expiresPtr,
	}
}

func writeReputationError(w http.ResponseWriter, id interface{}, err error) {
	if err == nil {
		return
	}
	status := http.StatusInternalServerError
	code := codeServerError
	message := "internal_error"
	data := err.Error()
	switch {
	case strings.Contains(strings.ToLower(err.Error()), "invalid"):
		status = http.StatusBadRequest
		code = codeInvalidParams
		message = "invalid_params"
	case strings.Contains(strings.ToLower(err.Error()), "unauthorized") || err == core.ErrReputationVerifierUnauthorized:
		status = http.StatusForbidden
		code = codeUnauthorized
		message = "forbidden"
	}
	writeError(w, status, id, code, message, data)
}
