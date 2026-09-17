package routes

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	lendingv1 "nhbchain/proto/lending/v1"
)

// authCheckingLendingServer rejects any RPC that doesn't carry the expected
// incoming "authorization" metadata -- standing in for the real lending
// service's own auth interceptor, so this test can prove the gateway
// actually forwards the caller's header rather than dropping it.
type authCheckingLendingServer struct {
	lendingv1.UnimplementedLendingServiceServer
}

func (authCheckingLendingServer) requireAuth(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		for _, v := range md.Get("authorization") {
			if strings.TrimSpace(v) == "Bearer secret-token" {
				return nil
			}
		}
	}
	return status.Error(codes.Unauthenticated, "missing or invalid authorization")
}

func (s authCheckingLendingServer) DepositCollateral(ctx context.Context, _ *lendingv1.DepositCollateralRequest) (*lendingv1.DepositCollateralResponse, error) {
	if err := s.requireAuth(ctx); err != nil {
		return nil, err
	}
	return &lendingv1.DepositCollateralResponse{}, nil
}

func (s authCheckingLendingServer) WithdrawCollateral(ctx context.Context, _ *lendingv1.WithdrawCollateralRequest) (*lendingv1.WithdrawCollateralResponse, error) {
	if err := s.requireAuth(ctx); err != nil {
		return nil, err
	}
	return &lendingv1.WithdrawCollateralResponse{}, nil
}

func (s authCheckingLendingServer) Liquidate(ctx context.Context, _ *lendingv1.LiquidateRequest) (*lendingv1.LiquidateResponse, error) {
	if err := s.requireAuth(ctx); err != nil {
		return nil, err
	}
	return &lendingv1.LiquidateResponse{}, nil
}

// NHB-AUDIT-S1: DepositCollateral/WithdrawCollateral/Liquidate had no HTTP
// route at all (gateway/routes/lending.go's mount only wired
// supply/withdraw/borrow/repay), and no lending route forwarded the
// caller's Authorization header to the outbound gRPC call, so the lending
// service's own auth interceptor always saw an empty request regardless
// of what the HTTP caller sent.
func TestLendingGatewayForwardsAuthorizationAndMountsCollateralRoutes(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	grpcServer := grpc.NewServer()
	lendingv1.RegisterLendingServiceServer(grpcServer, authCheckingLendingServer{})
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	target, err := url.Parse("http://" + lis.Addr().String())
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	lr, err := newLendingRoutes(target)
	if err != nil {
		t.Fatalf("new lending routes: %v", err)
	}

	router := chi.NewRouter()
	lr.mount(router)

	cases := []string{"/collateral/deposit", "/collateral/withdraw", "/liquidate"}

	for _, path := range cases {
		// No Authorization header -- the lending service's own auth check
		// must see nothing and reject.
		reqNoAuth := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		recNoAuth := httptest.NewRecorder()
		router.ServeHTTP(recNoAuth, reqNoAuth)
		if recNoAuth.Code == http.StatusNotFound {
			t.Fatalf("%s: route not mounted", path)
		}
		if recNoAuth.Code == http.StatusOK {
			t.Fatalf("%s: expected auth failure without a header, got 200", path)
		}

		// With the caller's Authorization header -- must reach the handler
		// as an authenticated call, proving the header was forwarded.
		reqAuth := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		reqAuth.Header.Set("Authorization", "Bearer secret-token")
		recAuth := httptest.NewRecorder()
		router.ServeHTTP(recAuth, reqAuth)
		if recAuth.Code != http.StatusOK {
			t.Fatalf("%s: expected 200 with forwarded auth header, got %d: %s", path, recAuth.Code, recAuth.Body.String())
		}
	}
}
