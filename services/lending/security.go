package main

import (
	"context"

	"google.golang.org/grpc"

	"nhbchain/network"
)

func newAuthInterceptors(auth network.Authenticator) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unary := func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if auth != nil {
			if err := auth.Authorize(ctx); err != nil {
				return nil, err
			}
		}
		return handler(ctx, req)
	}
	stream := func(srv interface{}, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if auth != nil {
			if err := auth.Authorize(ss.Context()); err != nil {
				return err
			}
		}
		return handler(srv, ss)
	}
	return unary, stream
}
