package server

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nhbchain/services/lending/engine"
)

func toStatus(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, engine.ErrNotFound):
		return status.Errorf(codes.NotFound, "resource not found")
	case errors.Is(err, engine.ErrPaused):
		return status.Errorf(codes.Unavailable, "operation paused")
	case errors.Is(err, engine.ErrUnauthorized):
		return status.Errorf(codes.PermissionDenied, "unauthorized")
	case errors.Is(err, engine.ErrInvalidAmount):
		return status.Errorf(codes.InvalidArgument, "invalid amount")
	case errors.Is(err, engine.ErrInsufficientCollateral):
		return status.Errorf(codes.ResourceExhausted, "insufficient collateral")
	case errors.Is(err, engine.ErrInternal):
		return status.Errorf(codes.Internal, "internal error")
	default:
		return status.Errorf(codes.Internal, "internal error")
	}
}
