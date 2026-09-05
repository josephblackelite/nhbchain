// Command gov-keeper is a small, standalone daemon that closes the gap the
// 2026-09-05 proposal-2 incident exposed: Finalize/Queue/Execute are each
// their own signed transaction (native/governance/engine.go), deliberately
// permissionless (no proposer/voter identity check), but nothing on-chain
// submits them automatically once a proposal's own timing condition is
// met -- a proposal can sit fully resolved in practice (voting closed,
// quorum decided) but stuck in "voting_period" status indefinitely if no
// human happens to notice and click a button. This daemon is that missing
// trigger: it polls gov_list on an interval and submits whichever of
// Finalize/Queue/Execute each proposal is currently eligible for, using the
// exact same signed-transaction wire format as cmd/nhb-cli's gov
// subcommand (see its sendGovTx) -- so it needs no special chain-side
// support, just a funded account to pay the (trivial, GasPrice=1) gas.
//
// It is deliberately NOT the only way a proposal advances: nhbportal's
// GovernanceHub also exposes a manual Finalize/Queue/Execute button
// (proposalAdvanceAction/handleAdvance) for the same three transaction
// types, so a proposal advances via whichever of the two happens first.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/rlp"
	"github.com/golang-jwt/jwt/v5"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// govProposalIDPayload mirrors cmd/nhb-cli/gov.go's identical type and
// core/governance_tx.go's unexported decode-side shape for
// TxTypeGovFinalize/Queue/Execute -- RLP encodes/decodes structs
// positionally, so this only needs to match structurally.
type govProposalIDPayload struct {
	ProposalID uint64
}

// govProposal mirrors the subset of rpc/gov handlers' gov_list/gov_proposal
// JSON shape this daemon actually needs to decide on an action -- see
// rpc/http.go's "gov_list" case and native/governance's proposal JSON
// marshaling.
type govProposal struct {
	ID          uint64 `json:"id"`
	Status      int    `json:"status"`
	VotingEnd   string `json:"voting_end"`
	TimelockEnd string `json:"timelock_end"`
	Queued      bool   `json:"queued"`
}

// Mirrors native/governance/types.go's ProposalStatus iota exactly --
// duplicated here rather than imported so this daemon stays a single,
// dependency-light file; keep in sync if that enum ever changes.
const (
	proposalStatusVotingPeriod = 2
	proposalStatusPassed       = 3
)

type govListResult struct {
	Proposals []govProposal `json:"proposals"`
}

type config struct {
	rpcEndpoint  string
	jwtSecretEnv string
	jwtIssuer    string
	jwtAudience  string
	keyPath      string
	pollInterval time.Duration
	listLimit    int
}

func main() {
	cfg := parseFlags()

	privKey, err := loadSignerKey(cfg.keyPath)
	if err != nil {
		log.Fatalf("gov-keeper: load signer key: %v", err)
	}
	address := privKey.PubKey().Address().String()
	log.Printf("gov-keeper: starting, signer=%s rpc=%s poll=%s", address, cfg.rpcEndpoint, cfg.pollInterval)

	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()

	tick(cfg, privKey)
	for range ticker.C {
		tick(cfg, privKey)
	}
}

// tick mints a fresh RPC token on every call rather than once at startup --
// this daemon is meant to run indefinitely (systemd Restart=on-failure only
// fires on a crash, not on a schedule), but mintJWT's token is only valid
// for 24h (see its own doc comment). A proposal's own voting/timelock
// window is routinely a week or more (VotingPeriodSeconds/TimelockSeconds
// in config.toml), so a token minted once at process start would expire
// long before the very proposals this daemon exists to finalize/queue/
// execute ever become eligible, silently disabling every RPC call for the
// rest of the process's life with no crash and no visible symptom short of
// reading these logs. A mint failure here (e.g. the JWT secret env var is
// transiently unreadable) just skips this tick and retries next interval,
// rather than crash-looping the whole daemon.
func tick(cfg config, privKey *crypto.PrivateKey) {
	token, err := mintJWT(cfg.jwtSecretEnv, cfg.jwtIssuer, cfg.jwtAudience)
	if err != nil {
		log.Printf("gov-keeper: mint RPC token: %v", err)
		return
	}
	client := &rpcClient{endpoint: cfg.rpcEndpoint, authToken: token}
	runOnce(client, privKey, cfg)
}

func parseFlags() config {
	var cfg config
	var pollSeconds int
	flag.StringVar(&cfg.rpcEndpoint, "rpc", envOr("GOV_KEEPER_RPC_URL", "http://127.0.0.1:8545"), "validator JSON-RPC endpoint")
	flag.StringVar(&cfg.jwtSecretEnv, "jwt-secret-env", envOr("GOV_KEEPER_JWT_SECRET_ENV", "NHB_RPC_JWT_SECRET"), "environment variable holding the RPC JWT HMAC secret")
	flag.StringVar(&cfg.jwtIssuer, "jwt-issuer", envOr("GOV_KEEPER_JWT_ISSUER", "nhb-rpc"), "RPC JWT issuer claim -- must match config.toml's [RPCJWT].Issuer")
	flag.StringVar(&cfg.jwtAudience, "jwt-audience", envOr("GOV_KEEPER_JWT_AUDIENCE", "wallets"), "RPC JWT audience claim -- must match config.toml's [RPCJWT].Audience")
	flag.StringVar(&cfg.keyPath, "key", envOr("GOV_KEEPER_KEY_PATH", ""), "path to the keeper's funded signer key file (required)")
	flag.IntVar(&pollSeconds, "poll-interval-seconds", 60, "how often to scan open proposals")
	flag.IntVar(&cfg.listLimit, "list-limit", 200, "max proposals fetched per poll -- MVP scope, no cursor pagination yet")
	flag.Parse()

	if strings.TrimSpace(cfg.keyPath) == "" {
		log.Fatal("gov-keeper: -key (or GOV_KEEPER_KEY_PATH) is required")
	}
	cfg.pollInterval = time.Duration(pollSeconds) * time.Second
	return cfg
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func loadSignerKey(path string) (*crypto.PrivateKey, error) {
	keyBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file %s: %w", path, err)
	}
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("key file %s is empty", path)
	}
	return crypto.PrivateKeyFromBytes(keyBytes)
}

// mintJWT mirrors generate_jwt.go's exact claim shape (iss/aud/iat/nbf/exp),
// just with a much shorter, refreshed-per-run lifetime isn't needed here --
// a long-lived token is fine since this process only ever holds it in
// memory and re-derives it fresh from the secret on every restart.
func mintJWT(secretEnv, issuer, audience string) (string, error) {
	secret := strings.TrimSpace(os.Getenv(secretEnv))
	if secret == "" {
		return "", fmt.Errorf("environment variable %s is empty", secretEnv)
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": issuer,
		"aud": []string{audience},
		"sub": "gov-keeper",
		"iat": now.Unix(),
		"nbf": now.Unix(),
		"exp": now.Add(24 * time.Hour).Unix(),
	})
	return token.SignedString([]byte(secret))
}

func runOnce(client *rpcClient, privKey *crypto.PrivateKey, cfg config) {
	proposals, err := client.listProposals(cfg.listLimit)
	if err != nil {
		log.Printf("gov-keeper: list proposals: %v", err)
		return
	}

	address := privKey.PubKey().Address().String()
	nonce, err := client.accountNonce(address)
	if err != nil {
		log.Printf("gov-keeper: fetch signer nonce: %v", err)
		return
	}

	now := time.Now().UTC()
	for _, p := range proposals {
		txType, label, ok := determineAction(p, now)
		if !ok {
			continue
		}
		hash, err := client.sendLifecycleTx(privKey, nonce, txType, p.ID)
		if err != nil {
			log.Printf("gov-keeper: proposal %d: %s failed: %v", p.ID, label, err)
			continue
		}
		log.Printf("gov-keeper: proposal %d: submitted %s (tx %s)", p.ID, label, hash)
		nonce++
	}
}

// determineAction mirrors nhbportal's GovernanceHub.svelte proposalAdvanceAction
// exactly -- both must independently arrive at the same eligibility decision
// for a given proposal, or the manual button and this daemon could race in
// confusing ways (they won't actually conflict on-chain, since the second of
// any duplicate submission just gets rejected as no-longer-eligible, but the
// intent is for both to agree on the FIRST valid action).
// Mirrors native/governance/types.go's remaining terminal ProposalStatus
// values -- Executed proposals keep queued=true forever (it records "was
// this ever queued", not "is a queue/execute step still pending"), so
// without this guard determineAction would try to re-execute every already
// -executed proposal on every single poll, forever (harmless -- the chain
// rejects it -- but pure noise in the logs).
const (
	proposalStatusRejected = 4
	proposalStatusFailed   = 5
	proposalStatusExpired  = 6
	proposalStatusExecuted = 7
)

func determineAction(p govProposal, now time.Time) (types.TxType, string, bool) {
	switch p.Status {
	case proposalStatusRejected, proposalStatusFailed, proposalStatusExpired, proposalStatusExecuted:
		return 0, "", false
	}
	switch {
	case p.Status == proposalStatusVotingPeriod:
		if p.VotingEnd == "" {
			return 0, "", false
		}
		votingEnd, err := time.Parse(time.RFC3339, p.VotingEnd)
		if err != nil || now.Before(votingEnd) {
			return 0, "", false
		}
		return types.TxTypeGovFinalize, "finalize", true
	case p.Queued:
		if p.TimelockEnd == "" {
			return 0, "", false
		}
		timelockEnd, err := time.Parse(time.RFC3339, p.TimelockEnd)
		if err != nil || now.Before(timelockEnd) {
			return 0, "", false
		}
		return types.TxTypeGovExecute, "execute", true
	case p.Status == proposalStatusPassed:
		return types.TxTypeGovQueue, "queue", true
	default:
		return 0, "", false
	}
}

type rpcClient struct {
	endpoint  string
	authToken string
}

func (c *rpcClient) listProposals(limit int) ([]govProposal, error) {
	params := map[string]interface{}{}
	if limit > 0 {
		params["limit"] = limit
	}
	var result govListResult
	if err := c.call("gov_list", []interface{}{params}, true, &result); err != nil {
		return nil, err
	}
	return result.Proposals, nil
}

func (c *rpcClient) accountNonce(address string) (uint64, error) {
	var result struct {
		Nonce uint64 `json:"nonce"`
	}
	if err := c.call("nhb_getBalance", []interface{}{address}, false, &result); err != nil {
		return 0, err
	}
	return result.Nonce, nil
}

func (c *rpcClient) sendLifecycleTx(privKey *crypto.PrivateKey, nonce uint64, txType types.TxType, proposalID uint64) (string, error) {
	data, err := rlp.EncodeToBytes(govProposalIDPayload{ProposalID: proposalID})
	if err != nil {
		return "", fmt.Errorf("encode payload: %w", err)
	}
	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    nonce,
		Data:     data,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(privKey.PrivateKey); err != nil {
		return "", fmt.Errorf("sign transaction: %w", err)
	}
	var result string
	if err := c.call("nhb_sendTransaction", []interface{}{&tx}, true, &result); err != nil {
		return "", err
	}
	return strings.TrimSpace(result), nil
}

func (c *rpcClient) call(method string, params []interface{}, requireAuth bool, out interface{}) error {
	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if requireAuth {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", c.endpoint, err)
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if rpcResp.Error != nil {
		if len(rpcResp.Error.Data) > 0 {
			var detail string
			if err := json.Unmarshal(rpcResp.Error.Data, &detail); err == nil && detail != "" {
				return fmt.Errorf("node error: %s: %s", rpcResp.Error.Message, detail)
			}
			return fmt.Errorf("node error: %s: %s", rpcResp.Error.Message, string(rpcResp.Error.Data))
		}
		return fmt.Errorf("node error: %s", rpcResp.Error.Message)
	}
	if out == nil {
		return nil
	}
	if len(rpcResp.Result) == 0 {
		return nil
	}
	return json.Unmarshal(rpcResp.Result, out)
}
