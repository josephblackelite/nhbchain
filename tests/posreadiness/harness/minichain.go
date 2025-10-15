package harness

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"nhbchain/core"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/rpc"
	"nhbchain/storage"
)

// MiniChain bootstraps a lightweight in-memory NHB chain suitable for POS readiness tests.
type MiniChain struct {
	db        storage.Database
	node      *core.Node
	rpcServer *rpc.Server
	rpcAddr   string

	shutdownOnce sync.Once
}

// NewMiniChain constructs an in-memory chain with a stub finalizer and an in-process RPC server.
func NewMiniChain() (*MiniChain, error) {
	db := storage.NewMemDB()

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("generate validator key: %w", err)
	}

	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create node: %w", err)
	}

	const jwtEnv = "MINICHAIN_RPC_JWT_SECRET"
	if err := os.Setenv(jwtEnv, "minichain-secret"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set JWT secret: %w", err)
	}
	server, err := rpc.NewServer(node, nil, rpc.ServerConfig{
		AllowInsecure: true,
		JWT: rpc.JWTConfig{
			Enable:      true,
			Alg:         "HS256",
			HSSecretEnv: jwtEnv,
			Issuer:      "minichain",
			Audience:    []string{"pos-tests"},
		},
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create rpc server: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("listen for RPC: %w", err)
	}

	addr := listener.Addr().String()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	if err := waitForDial(addr, 2*time.Second, serveErr); err != nil {
		_ = server.Shutdown(context.Background())
		_ = listener.Close()
		db.Close()
		return nil, err
	}

	return &MiniChain{
		db:        db,
		node:      node,
		rpcServer: server,
		rpcAddr:   addr,
	}, nil
}

// Node exposes the underlying core node.
func (mc *MiniChain) Node() *core.Node {
	if mc == nil {
		return nil
	}
	return mc.node
}

// RPCAddr returns the listening address of the in-process RPC server.
func (mc *MiniChain) RPCAddr() string {
	if mc == nil {
		return ""
	}
	return mc.rpcAddr
}

// FinalizeTxs builds and commits a block containing the supplied transactions.
func (mc *MiniChain) FinalizeTxs(txs ...*types.Transaction) (*types.Block, error) {
	if mc == nil || mc.node == nil {
		return nil, fmt.Errorf("minichain not initialised")
	}
	block, err := mc.node.CreateBlock(txs)
	if err != nil {
		return nil, fmt.Errorf("create block: %w", err)
	}
	if err := mc.node.CommitBlock(block); err != nil {
		return nil, fmt.Errorf("commit block: %w", err)
	}
	return block, nil
}

// Close releases all resources owned by the harness.
func (mc *MiniChain) Close() error {
	if mc == nil {
		return nil
	}
	var err error
	mc.shutdownOnce.Do(func() {
		if mc.rpcServer != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if shutdownErr := mc.rpcServer.Shutdown(shutdownCtx); shutdownErr != nil && !errors.Is(shutdownErr, context.Canceled) {
				err = fmt.Errorf("shutdown rpc: %w", shutdownErr)
			}
		}
		if mc.db != nil {
			mc.db.Close()
		}
	})
	return err
}

func waitForDial(addr string, timeout time.Duration, serveErr <-chan error) error {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("rpc server exited early: %w", err)
			}
			return nil
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dial %s: %w", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
