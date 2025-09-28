package stable

import "context"

// CashOutRequest represents a request to create a cash-out intent.
type CashOutRequest struct {
	ReservationID string
}

// CashOutResponse wraps the newly created intent.
type CashOutResponse struct {
	Intent CashOutIntent
}

// CashOut creates a placeholder intent from a reservation.
func (e *Engine) CashOut(ctx context.Context, req CashOutRequest) (CashOutResponse, error) {
	intent, err := e.CreateCashOutIntent(ctx, req.ReservationID)
	if err != nil {
		return CashOutResponse{}, err
	}
	return CashOutResponse{Intent: intent}, nil
}
