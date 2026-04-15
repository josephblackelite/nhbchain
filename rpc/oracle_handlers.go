package rpc

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleGetOraclePrice(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	asset, currency, err := parseOraclePriceParams(req.Params)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}

	engine, _, _, _ := s.stableEngineConfig()
	if engine == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "oracle unavailable", "stable engine not enabled")
		return
	}

	rate, observedAt, ok := engine.CurrentPrice(currency, asset)
	if !ok || rate <= 0 {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "oracle unavailable", "price unavailable")
		return
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	writeResult(w, req.ID, map[string]any{
		"asset":      asset,
		"currency":   currency,
		"usd":        rate,
		"price":      rate,
		"observedAt": observedAt.UTC().Format(time.RFC3339),
		"source":     "nhb-oracle",
	})
}

func parseOraclePriceParams(params []json.RawMessage) (string, string, error) {
	asset := ""
	currency := "USD"

	if len(params) == 0 {
		return "", "", errOracleParam("asset parameter required")
	}

	if len(params) == 1 {
		var payload struct {
			Asset    string `json:"asset"`
			Symbol   string `json:"symbol"`
			Token    string `json:"token"`
			Currency string `json:"currency"`
		}
		if err := json.Unmarshal(params[0], &payload); err == nil {
			asset = firstNonEmpty(payload.Asset, payload.Symbol, payload.Token)
			currency = firstNonEmpty(payload.Currency, currency)
		} else {
			if err := json.Unmarshal(params[0], &asset); err != nil {
				return "", "", errOracleParam("asset must be a string or object payload")
			}
		}
	} else {
		if err := json.Unmarshal(params[0], &asset); err != nil {
			return "", "", errOracleParam("asset must be a string")
		}
		if err := json.Unmarshal(params[1], &currency); err != nil {
			return "", "", errOracleParam("currency must be a string")
		}
	}

	asset = strings.ToUpper(strings.TrimSpace(asset))
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if asset == "" {
		return "", "", errOracleParam("asset parameter required")
	}
	if currency == "" {
		currency = "USD"
	}
	return asset, currency, nil
}

func errOracleParam(message string) error {
	return &rpcParamError{message: message}
}

type rpcParamError struct {
	message string
}

func (e *rpcParamError) Error() string {
	return e.message
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
