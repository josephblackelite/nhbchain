package stable

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

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

// CancelReservation releases the reservation and restores soft inventory and quotas.
func (e *Engine) CancelReservation(ctx context.Context, id string) error {
	if e == nil {
		return fmt.Errorf("engine not configured")
	}
	start := e.clock()
	ctx, span := e.tracer.Start(ctx, "stable.cancel_reservation",
		trace.WithAttributes(attribute.String("reservation.id", id)))
	defer span.End()
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.reserve[id]
	if !ok {
		err := ErrReservationNotFound
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cancel_reservation", e.clock().Sub(start), err)
		return err
	}
	now := e.clock()
	if err := e.releaseReservationLocked(state, now); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.metrics.Observe("cancel_reservation", e.clock().Sub(start), err)
		return err
	}
	delete(e.reserve, id)
	span.SetStatus(codes.Ok, "reservation cancelled")
	e.metrics.Observe("cancel_reservation", e.clock().Sub(start), nil)
	return nil
}
