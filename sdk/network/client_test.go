package network

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	networkv1 "nhbchain/proto/network/v1"
)

type noopNetworkServer struct {
	networkv1.UnimplementedNetworkServiceServer
}

func (noopNetworkServer) ListPeers(context.Context, *networkv1.ListPeersRequest) (*networkv1.ListPeersResponse, error) {
	return &networkv1.ListPeersResponse{}, nil
}

func (noopNetworkServer) GetView(context.Context, *networkv1.GetViewRequest) (*networkv1.GetViewResponse, error) {
	return &networkv1.GetViewResponse{View: &networkv1.NetworkView{}}, nil
}

func startInsecureServer(t *testing.T) (string, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := grpc.NewServer()
	networkv1.RegisterNetworkServiceServer(server, &noopNetworkServer{})

	go func() {
		_ = server.Serve(lis)
	}()

	cleanup := func() {
		server.Stop()
		_ = lis.Close()
	}

	return lis.Addr().String(), cleanup
}

func startProbeListener(t *testing.T) (string, <-chan []byte, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	data := make(chan []byte, 1)

	go func() {
		defer close(data)

		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 3)
		if n, err := conn.Read(buf); err == nil && n > 0 {
			data <- append([]byte{}, buf[:n]...)
		}
	}()

	cleanup := func() {
		_ = lis.Close()
	}

	return lis.Addr().String(), data, cleanup
}

func TestDialDefaultsToTLS(t *testing.T) {
	addr, handshake, cleanup := startProbeListener(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	select {
	case data := <-handshake:
		if len(data) == 0 || data[0] != 0x16 {
			t.Fatalf("expected TLS client hello, got %x", data)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TLS handshake data")
	}
}

func TestDialWithInsecureOptIn(t *testing.T) {
	addr, cleanup := startInsecureServer(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, err := Dial(ctx, addr, WithInsecure())
	if err != nil {
		t.Fatalf("dial with insecure opt-in: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	if _, err := client.ListPeers(ctx); err != nil {
		t.Fatalf("list peers: %v", err)
	}

	if _, err := client.GetView(ctx); err != nil {
		t.Fatalf("get view: %v", err)
	}
}
