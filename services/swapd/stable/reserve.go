package stable

import "context"

// ReserveRequest captures the required fields for a reservation.
type ReserveRequest struct {
	QuoteID  string
	Account  string
	AmountIn float64
}

// ReserveResponse returns the confirmed reservation details.
type ReserveResponse struct {
	Reservation Reservation
}

// Reserve locks a quote and creates a reservation placeholder.
func (e *Engine) Reserve(ctx context.Context, req ReserveRequest) (ReserveResponse, error) {
	reservation, err := e.ReserveQuote(ctx, req.QuoteID, req.Account, req.AmountIn)
	if err != nil {
		return ReserveResponse{}, err
	}
	return ReserveResponse{Reservation: reservation}, nil
}
