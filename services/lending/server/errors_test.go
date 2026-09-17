package server

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nhbchain/services/lending/engine"
)

func TestToStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		code    codes.Code
		message string
	}{
		{
			name:    "not found",
			err:     fmt.Errorf("wrap: %w", engine.ErrNotFound),
			code:    codes.NotFound,
			message: "resource not found",
		},
		{
			name:    "paused",
			err:     fmt.Errorf("wrap: %w", engine.ErrPaused),
			code:    codes.Unavailable,
			message: "operation paused",
		},
		{
			name:    "unauthorized",
			err:     fmt.Errorf("wrap: %w", engine.ErrUnauthorized),
			code:    codes.PermissionDenied,
			message: "unauthorized",
		},
		{
			name:    "invalid amount",
			err:     fmt.Errorf("wrap: %w", engine.ErrInvalidAmount),
			code:    codes.InvalidArgument,
			message: "invalid amount",
		},
		{
			name:    "insufficient collateral",
			err:     fmt.Errorf("wrap: %w", engine.ErrInsufficientCollateral),
			code:    codes.ResourceExhausted,
			message: "insufficient collateral",
		},
		{
			name:    "internal",
			err:     fmt.Errorf("wrap: %w", engine.ErrInternal),
			code:    codes.Internal,
			message: "internal error",
		},
		{
			name:    "unknown",
			err:     errors.New("boom"),
			code:    codes.Internal,
			message: "internal error",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := toStatus(tc.err)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("expected nil for nil error, got %v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected non-nil error")
			}
			st := status.Convert(got)
			if st.Code() != tc.code {
				t.Fatalf("expected code %s, got %s", tc.code, st.Code())
			}
			if st.Message() != tc.message {
				t.Fatalf("expected message %q, got %q", tc.message, st.Message())
			}
		})
	}

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		if toStatus(nil) != nil {
			t.Fatal("expected nil for nil error")
		}
	})
}
