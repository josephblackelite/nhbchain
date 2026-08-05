package server

import (
	"context"
	"errors"
	"log/slog"
	"math/big"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lendingv1 "nhbchain/proto/lending/v1"
	"nhbchain/services/lending/engine"
)

// Service implements the lending.v1 gRPC interface and proxies requests into
// the lending engine.
type Service struct {
	lendingv1.UnimplementedLendingServiceServer

	engine engine.Engine
	logger *slog.Logger
	auth   Authorizer
}

// Authorizer evaluates whether an incoming request is permitted.
type Authorizer interface {
	Authorize(context.Context) error
}

type interceptorAuthorizer struct{}

// NewInterceptorAuthorizer constructs an Authorizer that trusts the
// authentication context installed by the gRPC interceptors.
func NewInterceptorAuthorizer() Authorizer {
	return interceptorAuthorizer{}
}

func (interceptorAuthorizer) Authorize(ctx context.Context) error {
	if isAuthenticated(ctx) {
		return nil
	}
	return status.Error(codes.Unauthenticated, "authentication required")
}

// New constructs a new lending service instance.
func New(engine engine.Engine, logger *slog.Logger, auth Authorizer) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{engine: engine, logger: logger, auth: auth}
}

// GetMarket returns the current snapshot for the requested market.
func (s *Service) GetMarket(ctx context.Context, req *lendingv1.GetMarketRequest) (*lendingv1.GetMarketResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	var symbol string
	if req != nil && req.GetKey() != nil {
		symbol = strings.TrimSpace(req.GetKey().GetSymbol())
	}
	snapshot, err := s.engine.GetMarket(ctx, symbol)
	if err != nil {
		return nil, s.translateEngineError("get_market", err)
	}
	market := toProtoMarket(snapshot)
	if market == nil {
		return &lendingv1.GetMarketResponse{}, nil
	}
	return &lendingv1.GetMarketResponse{Market: market}, nil
}

// ListMarkets enumerates configured markets.
func (s *Service) ListMarkets(ctx context.Context, _ *lendingv1.ListMarketsRequest) (*lendingv1.ListMarketsResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	snapshots, err := s.engine.ListMarkets(ctx)
	if err != nil {
		return nil, s.translateEngineError("list_markets", err)
	}
	markets := make([]*lendingv1.Market, 0, len(snapshots))
	for _, snap := range snapshots {
		if market := toProtoMarket(snap); market != nil {
			markets = append(markets, market)
		}
	}
	return &lendingv1.ListMarketsResponse{Markets: markets}, nil
}

// GetPosition fetches the recorded position for an account.
func (s *Service) GetPosition(ctx context.Context, req *lendingv1.GetPositionRequest) (*lendingv1.GetPositionResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	account := strings.TrimSpace(req.GetAccount())
	if account == "" {
		return nil, status.Error(codes.InvalidArgument, "account required")
	}
	position, err := s.engine.GetPosition(ctx, account, "")
	if err != nil {
		return nil, s.translateEngineError("get_position", err)
	}
	if position.Account == nil {
		return nil, status.Error(codes.NotFound, "position not found")
	}
	return &lendingv1.GetPositionResponse{Position: toProtoPosition(position)}, nil
}

// SupplyAsset relays the caller's pre-signed supply transaction to the node.
func (s *Service) SupplyAsset(ctx context.Context, req *lendingv1.SupplyAssetRequest) (*lendingv1.SupplyAssetResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	account, symbol, amount, signedTx, err := validateMutationRequest(req.GetAccount(), req.GetMarket(), req.GetAmount(), req.GetSignedTxJson())
	if err != nil {
		return nil, err
	}
	txHash, err := s.engine.Supply(ctx, account, symbol, amount, signedTx)
	if err != nil {
		return nil, s.translateEngineError("supply_asset", err)
	}
	return &lendingv1.SupplyAssetResponse{TxHash: txHash}, nil
}

// WithdrawAsset relays the caller's pre-signed withdraw transaction to the node.
func (s *Service) WithdrawAsset(ctx context.Context, req *lendingv1.WithdrawAssetRequest) (*lendingv1.WithdrawAssetResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	account, symbol, amount, signedTx, err := validateMutationRequest(req.GetAccount(), req.GetMarket(), req.GetAmount(), req.GetSignedTxJson())
	if err != nil {
		return nil, err
	}
	txHash, err := s.engine.Withdraw(ctx, account, symbol, amount, signedTx)
	if err != nil {
		return nil, s.translateEngineError("withdraw_asset", err)
	}
	return &lendingv1.WithdrawAssetResponse{TxHash: txHash}, nil
}

// BorrowAsset relays the caller's pre-signed borrow transaction to the node.
func (s *Service) BorrowAsset(ctx context.Context, req *lendingv1.BorrowAssetRequest) (*lendingv1.BorrowAssetResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	account, symbol, amount, signedTx, err := validateMutationRequest(req.GetAccount(), req.GetMarket(), req.GetAmount(), req.GetSignedTxJson())
	if err != nil {
		return nil, err
	}
	txHash, err := s.engine.Borrow(ctx, account, symbol, amount, signedTx)
	if err != nil {
		return nil, s.translateEngineError("borrow_asset", err)
	}
	return &lendingv1.BorrowAssetResponse{TxHash: txHash}, nil
}

// RepayAsset relays the caller's pre-signed repay transaction to the node.
func (s *Service) RepayAsset(ctx context.Context, req *lendingv1.RepayAssetRequest) (*lendingv1.RepayAssetResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	account, symbol, amount, signedTx, err := validateMutationRequest(req.GetAccount(), req.GetMarket(), req.GetAmount(), req.GetSignedTxJson())
	if err != nil {
		return nil, err
	}
	txHash, err := s.engine.Repay(ctx, account, symbol, amount, signedTx)
	if err != nil {
		return nil, s.translateEngineError("repay_asset", err)
	}
	return &lendingv1.RepayAssetResponse{TxHash: txHash}, nil
}

// DepositCollateral relays the caller's pre-signed ZNHB collateral deposit.
func (s *Service) DepositCollateral(ctx context.Context, req *lendingv1.DepositCollateralRequest) (*lendingv1.DepositCollateralResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	account, symbol, amount, signedTx, err := validateMutationRequest(req.GetAccount(), req.GetMarket(), req.GetAmount(), req.GetSignedTxJson())
	if err != nil {
		return nil, err
	}
	txHash, err := s.engine.DepositCollateral(ctx, account, symbol, amount, signedTx)
	if err != nil {
		return nil, s.translateEngineError("deposit_collateral", err)
	}
	return &lendingv1.DepositCollateralResponse{TxHash: txHash}, nil
}

// WithdrawCollateral relays the caller's pre-signed ZNHB collateral withdrawal.
func (s *Service) WithdrawCollateral(ctx context.Context, req *lendingv1.WithdrawCollateralRequest) (*lendingv1.WithdrawCollateralResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	account, symbol, amount, signedTx, err := validateMutationRequest(req.GetAccount(), req.GetMarket(), req.GetAmount(), req.GetSignedTxJson())
	if err != nil {
		return nil, err
	}
	txHash, err := s.engine.WithdrawCollateral(ctx, account, symbol, amount, signedTx)
	if err != nil {
		return nil, s.translateEngineError("withdraw_collateral", err)
	}
	return &lendingv1.WithdrawCollateralResponse{TxHash: txHash}, nil
}

// Liquidate relays a liquidator's pre-signed transaction repaying an
// unhealthy borrower's debt in exchange for a discounted share of their
// collateral. It is signed by the liquidator, not the borrower -- see
// core/lending_native.go's applyLendingLiquidate for why the borrower's own
// signature is neither required nor meaningful here.
func (s *Service) Liquidate(ctx context.Context, req *lendingv1.LiquidateRequest) (*lendingv1.LiquidateResponse, error) {
	if err := s.ensureEngine(); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	liquidator := strings.TrimSpace(req.GetLiquidator())
	if liquidator == "" {
		return nil, status.Error(codes.InvalidArgument, "liquidator required")
	}
	borrower := strings.TrimSpace(req.GetBorrower())
	if borrower == "" {
		return nil, status.Error(codes.InvalidArgument, "borrower required")
	}
	var symbol string
	if req.GetMarket() != nil {
		symbol = strings.TrimSpace(req.GetMarket().GetSymbol())
	}
	signedTx := strings.TrimSpace(req.GetSignedTxJson())
	if signedTx == "" {
		return nil, status.Error(codes.InvalidArgument, "signed transaction required")
	}
	txHash, err := s.engine.Liquidate(ctx, liquidator, borrower, symbol, signedTx)
	if err != nil {
		return nil, s.translateEngineError("liquidate", err)
	}
	return &lendingv1.LiquidateResponse{TxHash: txHash}, nil
}

func (s *Service) authorize(ctx context.Context) error {
	if s == nil {
		return status.Error(codes.Internal, "service not initialised")
	}
	if s.auth == nil {
		return nil
	}
	return s.auth.Authorize(ctx)
}

func (s *Service) ensureEngine() error {
	if s == nil || s.engine == nil {
		return status.Error(codes.FailedPrecondition, "lending engine unavailable")
	}
	return nil
}

func (s *Service) translateEngineError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	stErr := toStatus(err)
	if status.Code(stErr) == codes.Internal {
		s.log().Error("lending engine error", "action", action, "error", err)
	}
	return stErr
}

func (s *Service) log() *slog.Logger {
	if s != nil && s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// validateMutationRequest validates the logging/validation-only fields
// (account, market, amount) alongside the caller's signed transaction JSON,
// which is what actually authorizes the mutation on-chain -- see the Engine
// interface doc comment in services/lending/engine/engine.go.
func validateMutationRequest(account string, market *lendingv1.MarketKey, amount, signedTxJSON string) (string, string, string, string, error) {
	trimmedAccount := strings.TrimSpace(account)
	if trimmedAccount == "" {
		return "", "", "", "", status.Error(codes.InvalidArgument, "account required")
	}
	var symbol string
	if market != nil {
		symbol = strings.TrimSpace(market.GetSymbol())
	}
	if symbol == "" {
		return "", "", "", "", status.Error(codes.InvalidArgument, "market symbol required")
	}
	trimmedAmount := strings.TrimSpace(amount)
	if trimmedAmount == "" {
		return "", "", "", "", status.Error(codes.InvalidArgument, "amount required")
	}
	trimmedSignedTx := strings.TrimSpace(signedTxJSON)
	if trimmedSignedTx == "" {
		return "", "", "", "", status.Error(codes.InvalidArgument, "signed transaction required")
	}
	return trimmedAccount, symbol, trimmedAmount, trimmedSignedTx, nil
}

func toProtoMarket(snapshot engine.Market) *lendingv1.Market {
	if snapshot.Market == nil {
		return nil
	}
	symbol := strings.TrimSpace(snapshot.Market.PoolID)
	market := &lendingv1.Market{
		Key:              &lendingv1.MarketKey{Symbol: symbol},
		BaseAsset:        defaultBaseAsset,
		CollateralFactor: formatUint(snapshot.RiskParameters.MaxLTV),
		ReserveFactor:    formatUint(snapshot.Market.ReserveFactor),
		LiquidityIndex:   normalizeAmount(snapshot.Market.SupplyIndex),
		BorrowIndex:      normalizeAmount(snapshot.Market.BorrowIndex),
	}
	return market
}

func toProtoPosition(pos engine.Position) *lendingv1.AccountPosition {
	if pos.Account == nil {
		return nil
	}
	account := pos.Account
	return &lendingv1.AccountPosition{
		Account:      strings.TrimSpace(account.Address),
		Supplied:     normalizeAmount(account.SupplyShares),
		Borrowed:     normalizeAmount(account.DebtNHB),
		Collateral:   normalizeAmount(account.CollateralZNHB),
		HealthFactor: computeHealthFactor(account.CollateralZNHB, account.DebtNHB),
	}
}

func formatUint(value uint64) string {
	return strconv.FormatUint(value, 10)
}

func normalizeAmount(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "0"
	}
	return trimmed
}

func computeHealthFactor(collateral, debt string) string {
	collateralValue := parseAmount(collateral)
	debtValue := parseAmount(debt)
	if debtValue.Sign() <= 0 {
		return "0"
	}
	if collateralValue.Sign() < 0 {
		return "0"
	}
	rat := new(big.Rat).SetFrac(collateralValue, debtValue)
	decimal := rat.FloatString(18)
	decimal = strings.TrimRight(strings.TrimRight(decimal, "0"), ".")
	if decimal == "" {
		return "0"
	}
	return decimal
}

func parseAmount(value string) *big.Int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return big.NewInt(0)
	}
	parsed, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return big.NewInt(0)
	}
	return parsed
}

const defaultBaseAsset = "NHB"
