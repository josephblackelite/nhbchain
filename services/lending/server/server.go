package server

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lendingv1 "nhbchain/proto/lending/v1"
)

// Service implements the lending.v1 gRPC interface. The current implementation
// acts as a placeholder until the full lending engine integration is
// completed.
type Service struct {
	lendingv1.UnimplementedLendingServiceServer
}

// New constructs a new lending service instance.
func New() *Service {
	return &Service{}
}

// GetMarket is currently unimplemented.
func (s *Service) GetMarket(ctx context.Context, req *lendingv1.GetMarketRequest) (*lendingv1.GetMarketResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "GetMarket is not implemented yet")
}

// ListMarkets is currently unimplemented.
func (s *Service) ListMarkets(ctx context.Context, req *lendingv1.ListMarketsRequest) (*lendingv1.ListMarketsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "ListMarkets is not implemented yet")
}

// GetPosition is currently unimplemented.
func (s *Service) GetPosition(ctx context.Context, req *lendingv1.GetPositionRequest) (*lendingv1.GetPositionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "GetPosition is not implemented yet")
}

// SupplyAsset is currently unimplemented.
func (s *Service) SupplyAsset(ctx context.Context, req *lendingv1.SupplyAssetRequest) (*lendingv1.SupplyAssetResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "SupplyAsset is not implemented yet")
}

// WithdrawAsset is currently unimplemented.
func (s *Service) WithdrawAsset(ctx context.Context, req *lendingv1.WithdrawAssetRequest) (*lendingv1.WithdrawAssetResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "WithdrawAsset is not implemented yet")
}

// BorrowAsset is currently unimplemented.
func (s *Service) BorrowAsset(ctx context.Context, req *lendingv1.BorrowAssetRequest) (*lendingv1.BorrowAssetResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "BorrowAsset is not implemented yet")
}

// RepayAsset is currently unimplemented.
func (s *Service) RepayAsset(ctx context.Context, req *lendingv1.RepayAssetRequest) (*lendingv1.RepayAssetResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "RepayAsset is not implemented yet")
}
