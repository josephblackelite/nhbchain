package modules

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/core"
	"nhbchain/core/types"
	"nhbchain/crypto"

	"github.com/ethereum/go-ethereum/common"
)

// TransactionsModule surfaces transaction sponsorship diagnostics via RPC.
type TransactionsModule struct {
	node *core.Node
}

// NewTransactionsModule constructs a module for transaction-focused RPC helpers.
func NewTransactionsModule(node *core.Node) *TransactionsModule {
	return &TransactionsModule{node: node}
}

// SponsorshipPreviewResult summarises the sponsorship evaluation for a transaction payload.
type SponsorshipPreviewResult struct {
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	Sponsor           string `json:"sponsor,omitempty"`
	GasPriceWei       string `json:"gasPriceWei,omitempty"`
	RequiredBudgetWei string `json:"requiredBudgetWei,omitempty"`
	ModuleEnabled     bool   `json:"moduleEnabled"`
}

// SponsorshipConfigResult describes the current module configuration.
type SponsorshipConfigResult struct {
	Enabled   bool   `json:"enabled"`
	AdminRole string `json:"adminRole"`
}

type setSponsorshipParams struct {
	Caller  string `json:"caller"`
	Enabled bool   `json:"enabled"`
}

// PreviewSponsorship returns the sponsorship assessment for the provided transaction payload without executing it.
func (m *TransactionsModule) PreviewSponsorship(raw json.RawMessage) (*SponsorshipPreviewResult, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "transactions module not initialised"}
	}
	if len(raw) == 0 {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "transaction parameter required"}
	}
	var tx types.Transaction
	if err := json.Unmarshal(raw, &tx); err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid transaction", Data: err.Error()}
	}
	assessment, err := m.node.EvaluateSponsorship(&tx)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: err.Error()}
	}
	result := &SponsorshipPreviewResult{ModuleEnabled: m.node.PaymasterModuleEnabled()}
	if assessment != nil {
		result.Status = string(assessment.Status)
		result.Reason = assessment.Reason
		if assessment.Sponsor != (common.Address{}) {
			result.Sponsor = crypto.NewAddress(crypto.NHBPrefix, assessment.Sponsor.Bytes()).String()
		}
		if assessment.GasPrice != nil {
			result.GasPriceWei = new(big.Int).Set(assessment.GasPrice).String()
		}
		if assessment.GasCost != nil {
			result.RequiredBudgetWei = new(big.Int).Set(assessment.GasCost).String()
		}
	}
	return result, nil
}

// SetSponsorshipEnabled updates the paymaster module status after verifying the caller is authorised.
func (m *TransactionsModule) SetSponsorshipEnabled(raw json.RawMessage) (*SponsorshipConfigResult, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "transactions module not initialised"}
	}
	var params setSponsorshipParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid parameter object", Data: err.Error()}
	}
	caller := strings.TrimSpace(params.Caller)
	if caller == "" {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "caller required"}
	}
	decoded, err := crypto.DecodeAddress(caller)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid caller address", Data: err.Error()}
	}
	if err := m.node.SetPaymasterModuleEnabled(decoded.Bytes(), params.Enabled); err != nil {
		switch {
		case errors.Is(err, core.ErrPaymasterUnauthorized):
			return nil, &ModuleError{HTTPStatus: http.StatusForbidden, Code: codeInvalidParams, Message: err.Error()}
		default:
			return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: err.Error()}
		}
	}
	return &SponsorshipConfigResult{Enabled: m.node.PaymasterModuleEnabled(), AdminRole: "ROLE_PAYMASTER_ADMIN"}, nil
}

// SponsorshipConfig returns the current sponsorship configuration metadata.
func (m *TransactionsModule) SponsorshipConfig() (*SponsorshipConfigResult, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "transactions module not initialised"}
	}
	return &SponsorshipConfigResult{Enabled: m.node.PaymasterModuleEnabled(), AdminRole: "ROLE_PAYMASTER_ADMIN"}, nil
}
