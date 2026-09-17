package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type balanceResponse struct {
	Address            string          `json:"address"`
	BalanceNHB         string          `json:"balanceNHB"`
	BalanceZNHB        string          `json:"balanceZNHB"`
	Stake              string          `json:"stake"`
	LockedZNHB         string          `json:"lockedZNHB"`
	DelegatedValidator string          `json:"delegatedValidator"`
	PendingUnbonds     json.RawMessage `json:"pendingUnbonds"`
	Username           string          `json:"username"`
	Nonce              uint64          `json:"nonce"`
	EngagementScore    uint64          `json:"engagementScore"`
}

type transaction struct {
	Hash      string `json:"hash"`
	From      string `json:"from"`
	To        string `json:"to"`
	Nonce     uint64 `json:"nonce"`
	Value     string `json:"value"`
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
}

type restPage struct {
	Data   []map[string]any `json:"data"`
	Paging map[string]any   `json:"paging"`
}

func main() {
	address := strings.TrimSpace(os.Getenv("NHB_ADDRESS"))
	if address == "" {
		log.Fatal("NHB_ADDRESS environment variable is required")
	}

	rpcURL := strings.TrimSpace(os.Getenv("NHB_RPC_URL"))
	if rpcURL == "" {
		rpcURL = "https://rpc.testnet.nhbcoin.net"
	}

	apiBase := strings.TrimSpace(os.Getenv("NHB_API_BASE"))
	if apiBase == "" {
		apiBase = "https://api.nhbcoin.net/escrow/v1"
	}

	apiKey := strings.TrimSpace(os.Getenv("NHB_API_KEY"))
	apiSecret := strings.TrimSpace(os.Getenv("NHB_API_SECRET"))

	fmt.Printf("RPC base: %s\n", rpcURL)
	fmt.Printf("REST base: %s\n", apiBase)
	fmt.Printf("Address: %s\n\n", address)

	if err := runRPCBalance(rpcURL, address); err != nil {
		log.Fatalf("rpc balance query failed: %v", err)
	}

	if err := runLatestTransactions(rpcURL); err != nil {
		log.Fatalf("rpc latest transactions failed: %v", err)
	}

	if apiKey == "" || apiSecret == "" {
		log.Println("skipping REST escrow lookup: set NHB_API_KEY and NHB_API_SECRET to enable")
		return
	}

	if err := runEscrowLookup(apiBase, apiKey, apiSecret, address); err != nil {
		log.Fatalf("escrow lookup failed: %v", err)
	}
}

func runRPCBalance(rpcURL, address string) error {
	fmt.Println("==> nhb_getBalance")
	result, err := callRPC(rpcURL, "nhb_getBalance", []interface{}{address})
	if err != nil {
		return err
	}
	var balance balanceResponse
	if err := json.Unmarshal(result, &balance); err != nil {
		return fmt.Errorf("decode balance payload: %w", err)
	}
	prettyPrint(balance)
	fmt.Println()
	return nil
}

func runLatestTransactions(rpcURL string) error {
	fmt.Println("==> nhb_getLatestTransactions")
	result, err := callRPC(rpcURL, "nhb_getLatestTransactions", []interface{}{10})
	if err != nil {
		return err
	}
	var txs []transaction
	if err := json.Unmarshal(result, &txs); err != nil {
		return fmt.Errorf("decode transactions payload: %w", err)
	}
	if len(txs) == 0 {
		fmt.Println("no recent transactions returned")
	} else {
		for i, tx := range txs {
			fmt.Printf("%2d. %s -> %s (%s)\n", i+1, tx.From, tx.To, tx.Value)
		}
	}
	fmt.Println()
	return nil
}

func runEscrowLookup(apiBase, apiKey, apiSecret, address string) error {
	fmt.Println("==> GET /trades (escrow gateway)")
	params := url.Values{}
	params.Set("buyer", address)
	params.Set("status", "SETTLED")
	params.Set("limit", "5")

	body, status, err := restRequest("GET", apiBase, "/trades", params, "", apiKey, apiSecret)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("gateway returned status %d: %s", status, string(body))
	}
	var page restPage
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decode escrow response: %w", err)
	}
	if len(page.Data) == 0 {
		fmt.Println("no settled trades for buyer; try seller or remove status filter")
	} else {
		for _, trade := range page.Data {
			fmt.Printf("trade %v status %v amount %v\n", trade["id"], trade["status"], trade["amount"])
		}
	}
	fmt.Println()
	return nil
}

func callRPC(rpcURL, method string, params []interface{}) (json.RawMessage, error) {
	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(rpcURL, "application/json", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("rpc responded with status %d: %s", resp.StatusCode, string(body))
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if rpcResp.Result == nil {
		return nil, errors.New("rpc result is empty")
	}
	return rpcResp.Result, nil
}

func restRequest(method, apiBase, path string, query url.Values, body string, apiKey, apiSecret string) ([]byte, int, error) {
	baseURL, err := url.Parse(apiBase)
	if err != nil {
		return nil, 0, fmt.Errorf("parse NHB_API_BASE: %w", err)
	}
	full := *baseURL
	full.Path = strings.TrimSuffix(baseURL.Path, "/") + path
	full.RawQuery = query.Encode()

	var reqBody io.Reader
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		reqBody = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, full.String(), reqBody)
	if err != nil {
		return nil, 0, err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)
	canonical := full.EscapedPath()
	if full.RawQuery != "" {
		canonical += "?" + full.RawQuery
	}

	stringToSign := strings.Join([]string{method, canonical, body, timestamp}, "\n")
	mac := hmac.New(sha256.New, []byte(apiSecret))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Signature", signature)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return respBody, resp.StatusCode, nil
}

func prettyPrint(v interface{}) {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Println(v)
		return
	}
	fmt.Println(string(buf))
}
