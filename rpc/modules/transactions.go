package modules

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
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
	Status            string                   `json:"status"`
	Reason            string                   `json:"reason,omitempty"`
	Sponsor           string                   `json:"sponsor,omitempty"`
	GasPriceWei       string                   `json:"gasPriceWei,omitempty"`
	RequiredBudgetWei string                   `json:"requiredBudgetWei,omitempty"`
	ModuleEnabled     bool                     `json:"moduleEnabled"`
	Throttle          *SponsorshipThrottleInfo `json:"throttle,omitempty"`
}

// SponsorshipThrottleInfo surfaces throttle metadata for a rejected sponsorship attempt.
type SponsorshipThrottleInfo struct {
	Scope         string `json:"scope"`
	Merchant      string `json:"merchant,omitempty"`
	DeviceID      string `json:"deviceId,omitempty"`
	Day           string `json:"day,omitempty"`
	LimitWei      string `json:"limitWei,omitempty"`
	UsedBudgetWei string `json:"usedBudgetWei,omitempty"`
	AttemptWei    string `json:"attemptBudgetWei,omitempty"`
	TxCount       uint64 `json:"txCount,omitempty"`
	LimitTxCount  uint64 `json:"limitTxCount,omitempty"`
}

// SponsorshipCounterTotals aggregates usage statistics for a scope.
type SponsorshipCounterTotals struct {
	BudgetWei  string `json:"budgetWei"`
	ChargedWei string `json:"chargedWei"`
	TxCount    uint64 `json:"txCount"`
}

// SponsorshipCountersResult summarises usage counters for the requested scope and day.
type SponsorshipCountersResult struct {
	Day            string                    `json:"day"`
	Merchant       string                    `json:"merchant,omitempty"`
	DeviceID       string                    `json:"deviceId,omitempty"`
	MerchantTotals *SponsorshipCounterTotals `json:"merchantTotals,omitempty"`
	DeviceTotals   *SponsorshipCounterTotals `json:"deviceTotals,omitempty"`
	GlobalTotals   SponsorshipCounterTotals  `json:"globalTotals"`
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

// sponsorshipCountersParams captures optional filter inputs for counter queries.
type sponsorshipCountersParams struct {
	Merchant string `json:"merchant,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`
	Day      string `json:"day,omitempty"`
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
			result.Sponsor = crypto.MustNewAddress(crypto.NHBPrefix, assessment.Sponsor.Bytes()).String()
		}
		if assessment.GasPrice != nil {
			result.GasPriceWei = new(big.Int).Set(assessment.GasPrice).String()
		}
		if assessment.GasCost != nil {
			result.RequiredBudgetWei = new(big.Int).Set(assessment.GasCost).String()
		}
		if assessment.Throttle != nil {
			result.Throttle = encodeThrottle(assessment.Throttle)
		}
	}
	return result, nil
}

func encodeThrottle(throttle *core.PaymasterThrottle) *SponsorshipThrottleInfo {
	if throttle == nil {
		return nil
	}
	info := &SponsorshipThrottleInfo{
		Scope:        string(throttle.Scope),
		Merchant:     throttle.Merchant,
		DeviceID:     throttle.DeviceID,
		Day:          throttle.Day,
		TxCount:      throttle.TxCount,
		LimitTxCount: throttle.LimitTxCount,
	}
	if throttle.LimitWei != nil {
		info.LimitWei = new(big.Int).Set(throttle.LimitWei).String()
	}
	if throttle.UsedBudgetWei != nil {
		info.UsedBudgetWei = new(big.Int).Set(throttle.UsedBudgetWei).String()
	}
	if throttle.AttemptBudgetWei != nil {
		info.AttemptWei = new(big.Int).Set(throttle.AttemptBudgetWei).String()
	}
	return info
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

// SponsorshipCounters returns usage counters for the requested scopes and day.
func (m *TransactionsModule) SponsorshipCounters(raw json.RawMessage) (*SponsorshipCountersResult, *ModuleError) {
	if m == nil || m.node == nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: "transactions module not initialised"}
	}
	var params sponsorshipCountersParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid parameter object", Data: err.Error()}
		}
	}
	day := strings.TrimSpace(params.Day)
	if day == "" {
		day = time.Now().UTC().Format(nhbstate.PaymasterDayFormat)
	} else {
		if _, err := time.Parse(nhbstate.PaymasterDayFormat, day); err != nil {
			return nil, &ModuleError{HTTPStatus: http.StatusBadRequest, Code: codeInvalidParams, Message: "invalid day format", Data: err.Error()}
		}
	}
	snapshot, err := m.node.PaymasterCounters(params.Merchant, params.DeviceID, day)
	if err != nil {
		return nil, &ModuleError{HTTPStatus: http.StatusInternalServerError, Code: codeServerError, Message: err.Error()}
	}
	result := &SponsorshipCountersResult{Day: snapshot.Day, Merchant: snapshot.Merchant, DeviceID: snapshot.DeviceID}
	result.GlobalTotals = SponsorshipCounterTotals{
		BudgetWei:  bigIntToString(snapshot.GlobalBudgetWei),
		ChargedWei: bigIntToString(snapshot.GlobalChargedWei),
		TxCount:    snapshot.GlobalTxCount,
	}
	if snapshot.Merchant != "" {
		result.MerchantTotals = &SponsorshipCounterTotals{
			BudgetWei:  bigIntToString(snapshot.MerchantBudgetWei),
			ChargedWei: bigIntToString(snapshot.MerchantChargedWei),
			TxCount:    snapshot.MerchantTxCount,
		}
	}
	if snapshot.Merchant != "" && snapshot.DeviceID != "" {
		result.DeviceTotals = &SponsorshipCounterTotals{
			BudgetWei:  bigIntToString(snapshot.DeviceBudgetWei),
			ChargedWei: bigIntToString(snapshot.DeviceChargedWei),
			TxCount:    snapshot.DeviceTxCount,
		}
	}
	return result, nil
}

func bigIntToString(value *big.Int) string {
	if value == nil {
		return "0"
	}
	return new(big.Int).Set(value).String()
}
