package gov_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	govv1 "nhbchain/proto/gov/v1"
	"nhbchain/services/governd/config"
	"nhbchain/services/governd/server"
)

func TestMsgInterceptorsRejectUnauthenticatedRequests(t *testing.T) {
	t.Parallel()

	unary, _ := server.NewAuthInterceptors(config.AuthConfig{APITokens: []string{"secret"}})
	invoked := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		invoked = true
		return nil, nil
	}
	_, err := unary(
		context.Background(),
		&govv1.MsgSubmitProposal{},
		&grpc.UnaryServerInfo{FullMethod: "/nhbchain.gov.v1.Msg/SubmitProposal"},
		handler,
	)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated error, got %v", err)
	}
	if invoked {
		t.Fatalf("handler should not execute for unauthenticated requests")
	}
}

func TestServiceRejectsUnauthenticatedCalls(t *testing.T) {
	t.Parallel()

	svc := server.New(nil, nil, config.Config{ChainID: "localnet", NonceStart: 1})
	if _, err := svc.SubmitProposal(context.Background(), &govv1.MsgSubmitProposal{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated error, got %v", err)
	}
}
