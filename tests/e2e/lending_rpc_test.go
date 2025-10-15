package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
	"nhbchain/rpc"
	"nhbchain/storage"
)

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

var weiUnit = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

func weiString(n int64) string {
	return new(big.Int).Mul(big.NewInt(n), weiUnit).String()
}

func weiBig(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), weiUnit)
}

func TestLendingRPCEndpoints(t *testing.T) {
	const jwtEnv = "LENDING_RPC_JWT_SECRET"
	const jwtSecret = "lending-secret"
	t.Setenv(jwtEnv, jwtSecret)

	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	risk := lending.RiskParameters{MaxLTV: 7500, LiquidationThreshold: 8000, LiquidationBonus: 500, DeveloperFeeCapBps: 100}
	node.SetLendingRiskParameters(risk)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("user key: %v", err)
	}
	userAddr := userKey.PubKey().Address()
	feeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("fee key: %v", err)
	}
	feeAddr := feeKey.PubKey().Address()
	node.SetLendingDeveloperFee(100, feeAddr)

	liquidatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("liquidator key: %v", err)
	}
	liquidatorAddr := liquidatorKey.PubKey().Address()

	borrowerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("borrower key: %v", err)
	}
	borrowerAddr := borrowerKey.PubKey().Address()

	if err := seedLendingState(node, userAddr, liquidatorAddr, borrowerAddr); err != nil {
		t.Fatalf("seed lending state: %v", err)
	}

	server, err := rpc.NewServer(node, nil, rpc.ServerConfig{
		AllowInsecure: true,
		JWT: rpc.JWTConfig{
			Enable:         true,
			Alg:            "HS256",
			HSSecretEnv:    jwtEnv,
			Issuer:         "lending-tests",
			Audience:       []string{"lending-cli"},
			MaxSkewSeconds: 60,
		},
	})
	if err != nil {
		t.Fatalf("new rpc server: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	waitForServer(t, addr)

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://" + addr

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := server.Shutdown(ctx); shutdownErr != nil {
			t.Fatalf("shutdown RPC server: %v", shutdownErr)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve RPC: %v", err)
		}
	})

	token := issueLendingJWT(t, []byte(jwtSecret), "lending-tests", []string{"lending-cli"}, time.Hour)

	marketResp := callRPC(t, client, baseURL, token, "lending_getMarket", nil)
	var marketResult struct {
		RiskParameters lending.RiskParameters `json:"riskParameters"`
	}
	if err := json.Unmarshal(marketResp.Result, &marketResult); err != nil {
		t.Fatalf("decode market: %v", err)
	}
	if marketResult.RiskParameters.MaxLTV != risk.MaxLTV || marketResult.RiskParameters.LiquidationThreshold != risk.LiquidationThreshold {
		t.Fatalf("unexpected risk parameters: %+v", marketResult.RiskParameters)
	}

	userAddrStr := userAddr.String()

	poolsResp := callRPC(t, client, baseURL, token, "lend_getPools", nil)
	var poolsResult struct {
		Pools []struct {
			PoolID string `json:"poolID"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(poolsResp.Result, &poolsResult); err != nil {
		t.Fatalf("decode pools: %v", err)
	}
	if len(poolsResult.Pools) != 1 || poolsResult.Pools[0].PoolID != "default" {
		t.Fatalf("expected default pool, got %+v", poolsResult.Pools)
	}

	createResp := callRPC(t, client, baseURL, token, "lend_createPool", map[string]string{"poolId": "secondary", "developerOwner": userAddrStr})
	var createdResult struct {
		Market struct {
			PoolID string `json:"poolID"`
		} `json:"market"`
	}
	if err := json.Unmarshal(createResp.Result, &createdResult); err != nil {
		t.Fatalf("decode create pool: %v", err)
	}
	if createdResult.Market.PoolID != "secondary" {
		t.Fatalf("unexpected pool id in create response: %+v", createdResult.Market)
	}

	poolsResp = callRPC(t, client, baseURL, token, "lend_getPools", nil)
	if err := json.Unmarshal(poolsResp.Result, &poolsResult); err != nil {
		t.Fatalf("decode pools after create: %v", err)
	}
	if len(poolsResult.Pools) != 2 {
		t.Fatalf("expected two pools, got %+v", poolsResult.Pools)
	}

	callRPC(t, client, baseURL, token, "lending_supplyNHB", map[string]string{"from": userAddrStr, "amount": weiString(1000)})
	callRPC(t, client, baseURL, token, "lending_depositZNHB", map[string]string{"from": userAddrStr, "amount": weiString(600)})
	callRPC(t, client, baseURL, token, "lending_borrowNHB", map[string]string{"borrower": userAddrStr, "amount": weiString(400)})
	callRPC(t, client, baseURL, token, "lending_repayNHB", map[string]string{"from": userAddrStr, "amount": weiString(400)})
	callRPC(t, client, baseURL, token, "lending_borrowNHBWithFee", map[string]interface{}{"borrower": userAddrStr, "amount": weiString(100)})
	callRPC(t, client, baseURL, token, "lending_repayNHB", map[string]string{"from": userAddrStr, "amount": weiString(101)})
	callRPC(t, client, baseURL, token, "lending_withdrawNHB", map[string]string{"from": userAddrStr, "amount": weiString(500)})
	callRPC(t, client, baseURL, token, "lending_withdrawZNHB", map[string]string{"from": userAddrStr, "amount": weiString(300)})

	accountResp := callRPC(t, client, baseURL, token, "lending_getUserAccount", userAddrStr)
	var accountResult struct {
		Account struct {
			CollateralZNHB *big.Int `json:"collateralZNHB"`
			SupplyShares   *big.Int `json:"supplyShares"`
			DebtNHB        *big.Int `json:"debtNHB"`
		} `json:"account"`
	}
	if err := json.Unmarshal(accountResp.Result, &accountResult); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if accountResult.Account.SupplyShares == nil || accountResult.Account.SupplyShares.Cmp(weiBig(500)) != 0 {
		t.Fatalf("unexpected supply shares: %v", accountResult.Account.SupplyShares)
	}
	if accountResult.Account.CollateralZNHB == nil || accountResult.Account.CollateralZNHB.Cmp(weiBig(300)) != 0 {
		t.Fatalf("unexpected collateral: %v", accountResult.Account.CollateralZNHB)
	}
	if accountResult.Account.DebtNHB == nil || accountResult.Account.DebtNHB.Sign() != 0 {
		t.Fatalf("expected zero debt, got %v", accountResult.Account.DebtNHB)
	}

	callRPC(t, client, baseURL, token, "lending_liquidate", map[string]string{"liquidator": liquidatorAddr.String(), "borrower": borrowerAddr.String()})

	borrowerResp := callRPC(t, client, baseURL, token, "lending_getUserAccount", borrowerAddr.String())
	var borrowerResult struct {
		Account struct {
			CollateralZNHB *big.Int `json:"collateralZNHB"`
			DebtNHB        *big.Int `json:"debtNHB"`
		} `json:"account"`
	}
	if err := json.Unmarshal(borrowerResp.Result, &borrowerResult); err != nil {
		t.Fatalf("decode borrower: %v", err)
	}
	if borrowerResult.Account.DebtNHB == nil || borrowerResult.Account.DebtNHB.Sign() != 0 {
		t.Fatalf("expected borrower debt cleared, got %v", borrowerResult.Account.DebtNHB)
	}
	if borrowerResult.Account.CollateralZNHB == nil || borrowerResult.Account.CollateralZNHB.Cmp(weiBig(150)) >= 0 {
		t.Fatalf("expected borrower collateral reduced, got %v", borrowerResult.Account.CollateralZNHB)
	}

	err = node.WithState(func(manager *nhbstate.Manager) error {
		liquidatorAccount, accErr := manager.GetAccount(liquidatorAddr.Bytes())
		if accErr != nil {
			return accErr
		}
		if liquidatorAccount.BalanceZNHB.Sign() == 0 {
			t.Fatalf("expected liquidator to receive collateral")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify liquidator: %v", err)
	}
}

func seedLendingState(node *core.Node, userAddr, liquidatorAddr, borrowerAddr crypto.Address) error {
	return node.WithState(func(manager *nhbstate.Manager) error {
		userAccount := &types.Account{BalanceNHB: weiBig(2000), BalanceZNHB: weiBig(1000)}
		if err := manager.PutAccount(userAddr.Bytes(), userAccount); err != nil {
			return err
		}

		moduleAddr := node.LendingModuleAddress()
		moduleAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
		if err := manager.PutAccount(moduleAddr.Bytes(), moduleAccount); err != nil {
			return err
		}

		collateralAddr := node.LendingCollateralAddress()
		collateralAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
		if err := manager.PutAccount(collateralAddr.Bytes(), collateralAccount); err != nil {
			return err
		}

		liquidatorAccount := &types.Account{BalanceNHB: weiBig(500), BalanceZNHB: big.NewInt(0)}
		if err := manager.PutAccount(liquidatorAddr.Bytes(), liquidatorAccount); err != nil {
			return err
		}

		borrowerAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
		if err := manager.PutAccount(borrowerAddr.Bytes(), borrowerAccount); err != nil {
			return err
		}

		poolID := "default"
		feeBps, feeCollector := node.LendingDeveloperFeeConfig()
		market := &lending.Market{DeveloperOwner: userAddr, DeveloperFeeBps: feeBps, DeveloperFeeCollector: feeCollector}
		if err := manager.LendingPutMarket(poolID, market); err != nil {
			return err
		}

		unhealthy := &lending.UserAccount{
			Address:        borrowerAddr,
			CollateralZNHB: weiBig(100),
			DebtNHB:        weiBig(120),
			ScaledDebt:     weiBig(120),
		}
		if err := manager.LendingPutUserAccount(poolID, unhealthy); err != nil {
			return err
		}

		updatedMarket, ok, err := manager.LendingGetMarket(poolID)
		if err != nil {
			return err
		}
		if !ok || updatedMarket == nil {
			updatedMarket = &lending.Market{}
		}
		updatedMarket.TotalNHBBorrowed = weiBig(120)
		updatedMarket.TotalNHBSupplied = weiBig(500)
		if err := manager.LendingPutMarket(poolID, updatedMarket); err != nil {
			return err
		}
		collateralAccount.BalanceZNHB = new(big.Int).Add(collateralAccount.BalanceZNHB, weiBig(100))
		if err := manager.PutAccount(collateralAddr.Bytes(), collateralAccount); err != nil {
			return err
		}
		return nil
	})
}

func callRPC(t *testing.T, client *http.Client, url, token, method string, params interface{}) rpcResponse {
	t.Helper()
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  []interface{}{},
	}
	switch v := params.(type) {
	case nil:
	case string:
		payload["params"] = []interface{}{v}
	default:
		payload["params"] = []interface{}{v}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("rpc request: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d for method %s: %s", resp.StatusCode, method, string(bodyBytes))
	}

	var parsed rpcResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if parsed.Error != nil {
		t.Fatalf("rpc error for %s: %+v", method, parsed.Error)
	}
	return parsed
}

func issueLendingJWT(t *testing.T, secret []byte, issuer string, audience []string, lifetime time.Duration) string {
	t.Helper()
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer:    issuer,
		Audience:  jwt.ClaimStrings(audience),
		ExpiresAt: jwt.NewNumericDate(now.Add(lifetime)),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

func waitForServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("rpc server did not start on %s: %v", addr, lastErr)
}
