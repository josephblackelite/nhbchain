package stable

import "context"

// QuoteRequest describes input for a quote calculation.
type QuoteRequest struct {
	Asset  string
	Amount float64
}

// QuoteResponse wraps the result of a quote lookup.
type QuoteResponse struct {
	Quote Quote
}

// Price computes a quote using the engine.
func (e *Engine) Price(ctx context.Context, req QuoteRequest) (QuoteResponse, error) {
	quote, err := e.PriceQuote(ctx, req.Asset, req.Amount)
	if err != nil {
		return QuoteResponse{}, err
	}
	return QuoteResponse{Quote: quote}, nil
}
