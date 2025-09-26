package server

import (
	"fmt"

	"nhbchain/services/otc-gateway/models"
)

var allowedTransitions = map[models.InvoiceState][]models.InvoiceState{
	models.StateCreated:         {models.StateReceiptUploaded},
	models.StateReceiptUploaded: {models.StatePendingReview},
	models.StatePendingReview:   {models.StateApproved, models.StateRejected},
	models.StateApproved:        {models.StateSigned, models.StateRejected},
	models.StateSigned:          {models.StateSubmitted, models.StateRejected},
	models.StateSubmitted:       {models.StateMinted},
}

// ValidateTransition ensures the transition follows the defined state machine.
func ValidateTransition(current, next models.InvoiceState) error {
	if current == next {
		return nil
	}
	allowed, ok := allowedTransitions[current]
	if !ok {
		return fmt.Errorf("no transitions allowed from %s", current)
	}
	for _, state := range allowed {
		if state == next {
			return nil
		}
	}
	return fmt.Errorf("transition from %s to %s is not permitted", current, next)
}
