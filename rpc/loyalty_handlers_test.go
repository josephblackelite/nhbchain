package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	gatewayauth "nhbchain/gateway/auth"
	"nhbchain/native/loyalty"
	swap "nhbchain/native/swap"
	"nhbchain/storage"
)

type testEnv struct {
	server     *Server
	node       *core.Node
	token      string
	swapKey    string
	swapSecret string
	now        time.Time
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := storage.NewMemDB()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	node, err := core.NewNode(db, key, "", true, true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	node.SetSwapConfig(swap.Config{AllowedFiat: []string{"USD"}, MaxQuoteAgeSeconds: 120, SlippageBps: 50, OraclePriority: []string{"manual"}})
	manual := swap.NewManualOracle()
	agg := swap.NewOracleAggregator([]string{"manual"}, 5*time.Minute)
	agg.Register("manual", manual)
	node.SetSwapOracle(agg)
	node.SetSwapManualOracle(manual)

	now := time.Unix(1700000000, 0).UTC()
	env := &testEnv{now: now, swapKey: "partner", swapSecret: "secret"}
	env.token = signEnvJWT(t, now)
	server := newTestServer(t, node, nil, ServerConfig{
		SwapAuth: SwapAuthConfig{
			Secrets:              map[string]string{env.swapKey: env.swapSecret},
			AllowedTimestampSkew: time.Minute,
			NonceTTL:             2 * time.Minute,
			NonceCapacity:        1024,
			RateLimitWindow:      time.Minute,
			PartnerRateLimits:    map[string]int{env.swapKey: 100},
			Now: func() time.Time {
				return env.now
			},
		},
	})
	env.server = server
	if server.jwtVerifier != nil {
		server.jwtVerifier.now = func() time.Time { return env.now }
	}
	env.node = node
	var treasury [20]byte
	treasury[19] = 0xEE
	cfg := env.node.PotsoRewardConfig()
	cfg.TreasuryAddress = treasury
	if err := env.node.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("configure rewards treasury: %v", err)
	}
	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		account := &types.Account{
			BalanceNHB:  big.NewInt(0),
			BalanceZNHB: new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil),
			Stake:       big.NewInt(0),
		}
		return manager.PutAccount(treasury[:], account)
	}); err != nil {
		t.Fatalf("seed rewards treasury: %v", err)
	}
	return env
}

func signEnvJWT(t *testing.T, now time.Time) string {
	t.Helper()
	claims := jwt.RegisteredClaims{
		Issuer:    "rpc-tests",
		Audience:  jwt.ClaimStrings([]string{"unit-tests"}),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

func (env *testEnv) newRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+env.token)
	return req
}

func (env *testEnv) signSwapRequest(t *testing.T, req *http.Request, body []byte, nonce string) {
	t.Helper()
	if req == nil {
		t.Fatalf("request is nil")
	}
	timestamp := strconv.FormatInt(env.now.Unix(), 10)
	sig := gatewayauth.ComputeSignature(env.swapSecret, timestamp, nonce, req.Method, gatewayauth.CanonicalRequestPath(req), body)
	req.Header.Set(gatewayauth.HeaderAPIKey, env.swapKey)
	req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(sig))
}

func marshalParam(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal param: %v", err)
	}
	return raw
}

func decodeRPCResponse(t *testing.T, rec *httptest.ResponseRecorder) (json.RawMessage, *RPCError) {
	t.Helper()
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.Result, resp.Error
}

func decodeBusinessID(t *testing.T, idStr string) loyalty.BusinessID {
	t.Helper()
	id, err := parseBusinessID(idStr)
	if err != nil {
		t.Fatalf("parse business id: %v", err)
	}
	return id
}

func decodeProgramID(t *testing.T, idStr string) loyalty.ProgramID {
	id, err := parseProgramID(idStr)
	if err != nil {
		t.Fatalf("parse program id: %v", err)
	}
	return id
}

func TestHandleLoyaltyCreateBusinessSuccess(t *testing.T) {
	env := newTestEnv(t)
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner: %v", err)
	}
	ownerAddr := ownerKey.PubKey().Address().String()

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller": ownerAddr,
		"name":   "Acme Corp",
	})}}
	recorder := httptest.NewRecorder()
	env.server.handleLoyaltyCreateBusiness(recorder, env.newRequest(), req)

	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var businessID string
	if err := json.Unmarshal(result, &businessID); err != nil {
		t.Fatalf("decode business id: %v", err)
	}
	id := decodeBusinessID(t, businessID)
	business, ok, err := env.node.LoyaltyBusinessByID(id)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	if !ok {
		t.Fatalf("business not found")
	}
	if business.Name != "Acme Corp" {
		t.Fatalf("unexpected business name: %s", business.Name)
	}
	if crypto.MustNewAddress(crypto.NHBPrefix, business.Owner[:]).String() != ownerAddr {
		t.Fatalf("owner mismatch")
	}
}

func TestHandleLoyaltyCreateBusinessInvalidAddress(t *testing.T) {
	env := newTestEnv(t)
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller": "invalid",
		"name":   "Bad",
	})}}
	recorder := httptest.NewRecorder()
	env.server.handleLoyaltyCreateBusiness(recorder, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr == nil {
		t.Fatalf("expected error")
	}
	if rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params code, got %d", rpcErr.Code)
	}
}

func TestHandleLoyaltySetPaymasterUnauthorized(t *testing.T) {
	env := newTestEnv(t)
	ownerKey, _ := crypto.GeneratePrivateKey()
	ownerAddr := ownerKey.PubKey().Address().String()
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller": ownerAddr,
		"name":   "Biz",
	})}}
	recorder := httptest.NewRecorder()
	env.server.handleLoyaltyCreateBusiness(recorder, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected error creating business: %+v", rpcErr)
	}
	var businessID string
	if err := json.Unmarshal(result, &businessID); err != nil {
		t.Fatalf("decode business id: %v", err)
	}

	outsiderKey, _ := crypto.GeneratePrivateKey()
	outsiderAddr := outsiderKey.PubKey().Address().String()
	payload := map[string]string{
		"caller":     outsiderAddr,
		"businessId": businessID,
		"paymaster":  ownerAddr,
	}
	setReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, payload)}}
	setRecorder := httptest.NewRecorder()
	env.server.handleLoyaltySetPaymaster(setRecorder, env.newRequest(), setReq)
	_, setErr := decodeRPCResponse(t, setRecorder)
	if setErr == nil {
		t.Fatalf("expected unauthorized error")
	}
	if setErr.Code != codeUnauthorized {
		t.Fatalf("expected code %d got %d", codeUnauthorized, setErr.Code)
	}
}

func TestHandleLoyaltyCreateProgramSuccess(t *testing.T) {
	env := newTestEnv(t)
	manager := env.node.LoyaltyManager()
	if err := manager.RegisterToken("ZNHB", "Zap", 18); err != nil {
		t.Fatalf("register token: %v", err)
	}
	ownerKey, _ := crypto.GeneratePrivateKey()
	ownerAddr := ownerKey.PubKey().Address().String()
	businessReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller": ownerAddr,
		"name":   "Rewards",
	})}}
	bizRec := httptest.NewRecorder()
	env.server.handleLoyaltyCreateBusiness(bizRec, env.newRequest(), businessReq)
	bizResult, bizErr := decodeRPCResponse(t, bizRec)
	if bizErr != nil {
		t.Fatalf("create business: %+v", bizErr)
	}
	var businessID string
	if err := json.Unmarshal(bizResult, &businessID); err != nil {
		t.Fatalf("decode business id: %v", err)
	}

	merchantKey, _ := crypto.GeneratePrivateKey()
	merchantAddr := merchantKey.PubKey().Address().String()
	addReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller":     ownerAddr,
		"businessId": businessID,
		"merchant":   merchantAddr,
	})}}
	addRec := httptest.NewRecorder()
	env.server.handleLoyaltyAddMerchant(addRec, env.newRequest(), addReq)
	_, addErr := decodeRPCResponse(t, addRec)
	if addErr != nil {
		t.Fatalf("add merchant: %+v", addErr)
	}

	var programID [32]byte
	programID[31] = 1
	programIDHex := "0x" + hex.EncodeToString(programID[:])
	poolKey, _ := crypto.GeneratePrivateKey()
	poolAddr := poolKey.PubKey().Address().String()
	spec := map[string]interface{}{
		"id":              programIDHex,
		"owner":           merchantAddr,
		"pool":            poolAddr,
		"tokenSymbol":     "ZNHB",
		"accrualBps":      100,
		"dailyCapProgram": "1000000000000000000000",
	}
	envReq := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, map[string]interface{}{
		"caller":     merchantAddr,
		"businessId": businessID,
		"spec":       spec,
	})}}
	envRec := httptest.NewRecorder()
	env.server.handleLoyaltyCreateProgram(envRec, env.newRequest(), envReq)
	programResult, programErr := decodeRPCResponse(t, envRec)
	if programErr != nil {
		t.Fatalf("create program error: %+v", programErr)
	}
	var returnedID string
	if err := json.Unmarshal(programResult, &returnedID); err != nil {
		t.Fatalf("decode program id: %v", err)
	}
	if returnedID != programIDHex {
		t.Fatalf("unexpected program id: %s", returnedID)
	}
	loaded, ok, err := env.node.LoyaltyProgramByID(decodeProgramID(t, returnedID))
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	if !ok {
		t.Fatalf("program not found")
	}
	if !loaded.Active || loaded.AccrualBps != 100 {
		t.Fatalf("unexpected program state")
	}
}

func TestHandleLoyaltyProgramStatsNotFound(t *testing.T) {
	env := newTestEnv(t)
	var programID loyalty.ProgramID
	programID[31] = 0x99
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"programId": "0x" + hex.EncodeToString(programID[:]),
		"day":       "2024-01-10",
	})}}
	rec := httptest.NewRecorder()
	env.server.handleLoyaltyProgramStats(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected not-found error for unknown program")
	}
}

// TestHandleLoyaltyProgramStatsCapUsage covers the two honesty-relevant
// shapes of the response: a program with no configured DailyCapProgram must
// report capUsage as JSON null (not a fabricated "0" indistinguishable from
// a capped program with zero usage), while a program with a real daily cap
// must report a real fraction. rewardsPaid/txCount must reflect real meters
// in both cases since those are now written unconditionally. The "no daily
// cap" program still sets EpochCapProgram, since CreateProgram's anti-sybil
// rule requires at least one program-wide cap -- this is the realistic shape
// (epoch-capped, not day-capped) rather than a fully uncapped program, which
// can now only exist as a pre-anti-sybil-rule legacy record.
func TestHandleLoyaltyProgramStatsCapUsage(t *testing.T) {
	env := newTestEnv(t)
	manager := env.node.LoyaltyManager()

	var uncapped loyalty.ProgramID
	uncapped[10] = 0x01
	if err := manager.KVPut(loyalty.ProgramStorageKey(uncapped), &loyalty.Program{
		ID:                 uncapped,
		TokenSymbol:        "ZNHB",
		AccrualBps:         500,
		EpochCapProgram:    big.NewInt(5000),
		EpochLengthSeconds: 86400,
		Active:             true,
	}); err != nil {
		t.Fatalf("seed uncapped program: %v", err)
	}
	if err := manager.SetLoyaltyProgramDailyTotalAccrued(uncapped, "2024-01-10", big.NewInt(300)); err != nil {
		t.Fatalf("seed uncapped total: %v", err)
	}
	if err := manager.SetLoyaltyProgramDailyTxCount(uncapped, "2024-01-10", 3); err != nil {
		t.Fatalf("seed uncapped tx count: %v", err)
	}

	uncappedReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"programId": "0x" + hex.EncodeToString(uncapped[:]),
		"day":       "2024-01-10",
	})}}
	uncappedRec := httptest.NewRecorder()
	env.server.handleLoyaltyProgramStats(uncappedRec, env.newRequest(), uncappedReq)
	uncappedResult, uncappedErr := decodeRPCResponse(t, uncappedRec)
	if uncappedErr != nil {
		t.Fatalf("unexpected error: %+v", uncappedErr)
	}
	var uncappedStats programStatsResult
	if err := json.Unmarshal(uncappedResult, &uncappedStats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if uncappedStats.RewardsPaid != "300" {
		t.Fatalf("expected rewardsPaid 300, got %s", uncappedStats.RewardsPaid)
	}
	if uncappedStats.TxCount != "3" {
		t.Fatalf("expected txCount 3, got %s", uncappedStats.TxCount)
	}
	if uncappedStats.CapUsage != nil {
		t.Fatalf("expected capUsage null for a program with no configured daily cap, got %q", *uncappedStats.CapUsage)
	}

	var capped loyalty.ProgramID
	capped[10] = 0x02
	if err := manager.KVPut(loyalty.ProgramStorageKey(capped), &loyalty.Program{
		ID:              capped,
		TokenSymbol:     "ZNHB",
		AccrualBps:      500,
		DailyCapProgram: big.NewInt(1000),
		Active:          true,
	}); err != nil {
		t.Fatalf("seed capped program: %v", err)
	}
	if err := manager.SetLoyaltyProgramDailyTotalAccrued(capped, "2024-01-10", big.NewInt(250)); err != nil {
		t.Fatalf("seed capped total: %v", err)
	}

	cappedReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"programId": "0x" + hex.EncodeToString(capped[:]),
		"day":       "2024-01-10",
	})}}
	cappedRec := httptest.NewRecorder()
	env.server.handleLoyaltyProgramStats(cappedRec, env.newRequest(), cappedReq)
	cappedResult, cappedErr := decodeRPCResponse(t, cappedRec)
	if cappedErr != nil {
		t.Fatalf("unexpected error: %+v", cappedErr)
	}
	var cappedStats programStatsResult
	if err := json.Unmarshal(cappedResult, &cappedStats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if cappedStats.RewardsPaid != "250" {
		t.Fatalf("expected rewardsPaid 250, got %s", cappedStats.RewardsPaid)
	}
	if cappedStats.CapUsage == nil {
		t.Fatalf("expected capUsage to be populated for a capped program")
	}
	if *cappedStats.CapUsage != "0.2500" {
		t.Fatalf("expected capUsage 0.2500 (250/1000), got %s", *cappedStats.CapUsage)
	}
}

// TestHandleLoyaltyProgramStatsLifetimeRewardsPaid proves loyalty_programStats
// exposes a real, distinct lifetimeRewardsPaid field: cumulative across every
// day of accrual history, not scoped to the single `day` the request asked
// about (rewardsPaid/txCount) and not equal to it once history spans more
// than one day. It also covers a program with no lifetime accrual history
// reading back a clean "0", not an error or a missing field.
func TestHandleLoyaltyProgramStatsLifetimeRewardsPaid(t *testing.T) {
	env := newTestEnv(t)
	manager := env.node.LoyaltyManager()

	var programID loyalty.ProgramID
	programID[11] = 0x09
	if err := manager.KVPut(loyalty.ProgramStorageKey(programID), &loyalty.Program{
		ID:                 programID,
		TokenSymbol:        "ZNHB",
		AccrualBps:         500,
		EpochCapProgram:    big.NewInt(5000),
		EpochLengthSeconds: 86400,
		Active:             true,
	}); err != nil {
		t.Fatalf("seed program: %v", err)
	}

	// Seed history across two distinct days plus a lifetime total that is the
	// sum of both -- proving the RPC surfaces the lifetime meter directly
	// rather than deriving it from the requested day's total.
	if err := manager.SetLoyaltyProgramDailyTotalAccrued(programID, "2024-01-10", big.NewInt(300)); err != nil {
		t.Fatalf("seed day1 total: %v", err)
	}
	if err := manager.SetLoyaltyProgramDailyTotalAccrued(programID, "2024-01-11", big.NewInt(150)); err != nil {
		t.Fatalf("seed day2 total: %v", err)
	}
	if err := manager.SetLoyaltyProgramLifetimeAccrued(programID, big.NewInt(450)); err != nil {
		t.Fatalf("seed lifetime total: %v", err)
	}

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"programId": "0x" + hex.EncodeToString(programID[:]),
		"day":       "2024-01-10",
	})}}
	rec := httptest.NewRecorder()
	env.server.handleLoyaltyProgramStats(rec, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected error: %+v", rpcErr)
	}
	var stats programStatsResult
	if err := json.Unmarshal(result, &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.RewardsPaid != "300" {
		t.Fatalf("expected rewardsPaid (day-scoped) 300, got %s", stats.RewardsPaid)
	}
	if stats.LifetimeRewardsPaid != "450" {
		t.Fatalf("expected lifetimeRewardsPaid 450 (300+150), got %s", stats.LifetimeRewardsPaid)
	}
	if stats.LifetimeRewardsPaid == stats.RewardsPaid {
		t.Fatalf("lifetimeRewardsPaid must not collapse to the day-scoped rewardsPaid once history spans multiple days")
	}

	// A second, untouched program must report a clean zero lifetime total,
	// not an error and not a missing/null field.
	var freshProgram loyalty.ProgramID
	freshProgram[11] = 0x0A
	if err := manager.KVPut(loyalty.ProgramStorageKey(freshProgram), &loyalty.Program{
		ID:                 freshProgram,
		TokenSymbol:        "ZNHB",
		AccrualBps:         500,
		EpochCapProgram:    big.NewInt(5000),
		EpochLengthSeconds: 86400,
		Active:             true,
	}); err != nil {
		t.Fatalf("seed fresh program: %v", err)
	}
	freshReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"programId": "0x" + hex.EncodeToString(freshProgram[:]),
		"day":       "2024-01-10",
	})}}
	freshRec := httptest.NewRecorder()
	env.server.handleLoyaltyProgramStats(freshRec, env.newRequest(), freshReq)
	freshResult, freshErr := decodeRPCResponse(t, freshRec)
	if freshErr != nil {
		t.Fatalf("unexpected error for fresh program: %+v", freshErr)
	}
	var freshStats programStatsResult
	if err := json.Unmarshal(freshResult, &freshStats); err != nil {
		t.Fatalf("decode fresh stats: %v", err)
	}
	if freshStats.LifetimeRewardsPaid != "0" {
		t.Fatalf("expected lifetimeRewardsPaid 0 for untouched program, got %s", freshStats.LifetimeRewardsPaid)
	}
}

func TestHandleLoyaltyUpdateProgramUnauthorized(t *testing.T) {
	env := newTestEnv(t)
	manager := env.node.LoyaltyManager()
	if err := manager.RegisterToken("ZNHB", "Zap", 18); err != nil {
		t.Fatalf("register token: %v", err)
	}
	ownerKey, _ := crypto.GeneratePrivateKey()
	ownerAddr := ownerKey.PubKey().Address().String()
	businessReq := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller": ownerAddr,
		"name":   "Rewards",
	})}}
	bizRec := httptest.NewRecorder()
	env.server.handleLoyaltyCreateBusiness(bizRec, env.newRequest(), businessReq)
	bizResult, bizErr := decodeRPCResponse(t, bizRec)
	if bizErr != nil {
		t.Fatalf("create business: %+v", bizErr)
	}
	var businessID string
	if err := json.Unmarshal(bizResult, &businessID); err != nil {
		t.Fatalf("decode business id: %v", err)
	}

	merchantKey, _ := crypto.GeneratePrivateKey()
	merchantAddr := merchantKey.PubKey().Address().String()
	addReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"caller":     ownerAddr,
		"businessId": businessID,
		"merchant":   merchantAddr,
	})}}
	addRec := httptest.NewRecorder()
	env.server.handleLoyaltyAddMerchant(addRec, env.newRequest(), addReq)
	if _, addErr := decodeRPCResponse(t, addRec); addErr != nil {
		t.Fatalf("add merchant: %+v", addErr)
	}

	var programID [32]byte
	programID[31] = 1
	programIDHex := "0x" + hex.EncodeToString(programID[:])
	poolKey, _ := crypto.GeneratePrivateKey()
	poolAddr := poolKey.PubKey().Address().String()
	createSpec := map[string]interface{}{
		"id":              programIDHex,
		"owner":           merchantAddr,
		"pool":            poolAddr,
		"tokenSymbol":     "ZNHB",
		"accrualBps":      100,
		"dailyCapProgram": "1000000000000000000000",
	}
	createReq := &RPCRequest{ID: 3, Params: []json.RawMessage{marshalParam(t, map[string]interface{}{
		"caller":     merchantAddr,
		"businessId": businessID,
		"spec":       createSpec,
	})}}
	createRec := httptest.NewRecorder()
	env.server.handleLoyaltyCreateProgram(createRec, env.newRequest(), createReq)
	if _, createErr := decodeRPCResponse(t, createRec); createErr != nil {
		t.Fatalf("create program: %+v", createErr)
	}

	// An outsider -- neither the program's owner nor a ROLE_LOYALTY_ADMIN --
	// must not be able to update the program, even by declaring themselves
	// as both caller and spec owner (Owner is immutable and checked against
	// the real existing record, not the caller-supplied spec).
	outsiderKey, _ := crypto.GeneratePrivateKey()
	outsiderAddr := outsiderKey.PubKey().Address().String()
	updateSpec := map[string]interface{}{
		"id":              programIDHex,
		"owner":           merchantAddr,
		"pool":            poolAddr,
		"tokenSymbol":     "ZNHB",
		"accrualBps":      500,
		"dailyCapProgram": "1000000000000000000000",
	}
	updateReq := &RPCRequest{ID: 4, Params: []json.RawMessage{marshalParam(t, map[string]interface{}{
		"caller": outsiderAddr,
		"spec":   updateSpec,
	})}}
	updateRec := httptest.NewRecorder()
	env.server.handleLoyaltyUpdateProgram(updateRec, env.newRequest(), updateReq)
	_, updateErr := decodeRPCResponse(t, updateRec)
	if updateErr == nil {
		t.Fatalf("expected unauthorized error")
	}
	if updateErr.Code != codeUnauthorized {
		t.Fatalf("expected code %d got %d", codeUnauthorized, updateErr.Code)
	}

	loaded, ok, err := env.node.LoyaltyProgramByID(decodeProgramID(t, programIDHex))
	if err != nil {
		t.Fatalf("load program: %v", err)
	}
	if !ok {
		t.Fatalf("program not found")
	}
	if loaded.AccrualBps != 100 {
		t.Fatalf("unauthorized update must not apply: accrualBps = %d, want 100", loaded.AccrualBps)
	}
}
