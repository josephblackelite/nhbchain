package main

// A small local network for the end-to-end test: real nhb processes, started
// from a genesis derived from the shipped one, with the shipped config.toml and
// only node-local values changed.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// e2eBash returns the bash the end-to-end test runs the scripts with. As in
// tests/scripts, it must be named on Windows (Git Bash), where the bash first
// on PATH may be another one.
func e2eBash(t *testing.T) string {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv("NHB_TEST_BASH")); path != "" {
		return path
	}
	if runtime.GOOS == "windows" {
		t.Skip("the end-to-end test needs a POSIX bash; set NHB_TEST_BASH to run it on Windows")
	}
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	return path
}

func slash(p string) string { return filepath.ToSlash(p) }

type e2eKey struct {
	path string
	hex  string
	addr string
	raw  []byte
}

type e2eEnv struct {
	t       *testing.T
	dir     string
	repo    string
	bash    string
	nhb     string
	cli     string
	tool    string
	genesis string
	chainID uint64
	genHash []byte
	keys    map[string]*e2eKey
	token   string
	secret  string
	nodes   []*e2eNode
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func goBuild(t *testing.T, repo, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, output)
	}
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	e := &e2eEnv{t: t, repo: repoRoot(t), bash: e2eBash(t), keys: map[string]*e2eKey{}, secret: "devnet-jwt-secret-0123456789abcdef0123456789abcdef"}
	e.dir = t.TempDir()
	if keep := os.Getenv("NHB_SNAPSHOT_E2E_DIR"); keep != "" {
		if err := os.MkdirAll(keep, 0o755); err != nil {
			t.Fatal(err)
		}
		e.dir = keep
	}
	bin := filepath.Join(e.dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	e.nhb = filepath.Join(bin, "nhb"+ext)
	e.cli = filepath.Join(bin, "nhb-cli"+ext)
	e.tool = filepath.Join(bin, "nhb-snapshot"+ext)
	start := time.Now()
	goBuild(t, e.repo, e.nhb, "./cmd/nhb")
	goBuild(t, e.repo, e.cli, "./cmd/nhb-cli")
	goBuild(t, e.repo, e.tool, "./cmd/nhb-snapshot")
	t.Logf("built nhb, nhb-cli and nhb-snapshot in %s", time.Since(start).Round(time.Second))

	// Keys and genesis of an earlier run in the same directory are reused, so
	// a run can be repeated on the chain an earlier run left behind.
	for _, name := range []string{"validator", "follower", "alice", "bob", "second"} {
		path := filepath.Join(e.dir, "keys", name+".key")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		var key *crypto.PrivateKey
		if raw, err := os.ReadFile(path); err == nil {
			if key, err = crypto.PrivateKeyFromBytes(raw); err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
		} else {
			var genErr error
			if key, genErr = crypto.GeneratePrivateKey(); genErr != nil {
				t.Fatal(genErr)
			}
			if err := os.WriteFile(path, key.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		e.keys[name] = &e2eKey{path: path, hex: hex.EncodeToString(key.Bytes()), addr: key.PubKey().Address().String(), raw: key.Bytes()}
	}
	e.writeGenesis()
	t.Cleanup(e.stopAll)
	return e
}

// writeGenesis derives a devnet genesis from the shipped one: the same tokens,
// roles, treasuries and loyalty settings, with this network's validator and two
// funded test accounts. Its chain id differs from the live one because the
// genesis differs; nothing else about the chain does.
func (e *e2eEnv) writeGenesis() {
	t := e.t
	raw, err := os.ReadFile(filepath.Join(e.repo, "config", "genesis.relaunch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g map[string]any
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	validators := g["validators"].([]any)
	validators[0].(map[string]any)["address"] = e.keys["validator"].addr
	alloc := g["alloc"].(map[string]any)
	for _, name := range []string{"alice", "bob"} {
		alloc[e.keys[name].addr] = map[string]any{"NHB": "5000000000000000000000", "ZNHB": "5000000000000000000000"}
	}
	out, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	e.genesis = filepath.Join(e.dir, "genesis.devnet.json")
	if _, statErr := os.Stat(e.genesis); statErr != nil {
		if err := os.WriteFile(e.genesis, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The chain id and genesis hash a node started from this file reports,
	// worked out with the code a node uses, independently of any running node.
	db := storage.NewMemDB()
	defer db.Close()
	var bc *core.Blockchain
	quietly(func() { bc, err = core.NewBlockchain(db, e.genesis, false) })
	if err != nil {
		t.Fatalf("load devnet genesis: %v", err)
	}
	e.chainID, e.genHash = bc.ChainID(), bc.GenesisHash()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// configOverlay is what differs between the shipped config.toml and a node of
// this local network: where it listens and keeps its data, whom it dials, and
// timeouts short enough to make a few thousand blocks in a few minutes.
type configOverlay struct {
	dataDir  string
	genesis  string
	p2p, rpc int
	peer     string // host:port of the node to sync from, empty for none
	timeout  string
}

func setLine(t *testing.T, src, pattern, replacement string) string {
	t.Helper()
	re := regexp.MustCompile(pattern)
	if !re.MatchString(src) {
		t.Fatalf("the config template has no line matching %s", pattern)
	}
	return re.ReplaceAllLiteralString(src, replacement)
}

func renderDevnetConfig(t *testing.T, templatePath string, o configOverlay) string {
	t.Helper()
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	s = setLine(t, s, `(?m)^ListenAddress = ".*"`, fmt.Sprintf(`ListenAddress = "127.0.0.1:%d"`, o.p2p))
	s = setLine(t, s, `(?m)^RPCAddress = ".*"`, fmt.Sprintf(`RPCAddress = "127.0.0.1:%d"`, o.rpc))
	s = setLine(t, s, `(?m)^RPCTrustProxyHeaders = .*`, `RPCTrustProxyHeaders = false`)
	s = setLine(t, s, `(?m)^RPCTrustedProxies = .*`, `RPCTrustedProxies = []`)
	for _, key := range []string{"RPCMaxTxPerWindow", "RPCMaxTxPerIP", "RPCMaxTxPerIdentity", "RPCMaxTxPerChain", "RPCMaxTxPerIdentityChain"} {
		s = setLine(t, s, `(?m)^`+key+` = .*`, key+` = 1000000`)
	}
	s = setLine(t, s, `(?m)^DataDir = ".*"`, fmt.Sprintf(`DataDir = "%s"`, slash(o.dataDir)))
	s = setLine(t, s, `(?m)^GenesisFile = ".*"`, fmt.Sprintf(`GenesisFile = "%s"`, slash(o.genesis)))
	s = setLine(t, s, `(?m)^ValidatorKeystorePath = ".*"`, `ValidatorKeystorePath = ""`)
	s = setLine(t, s, `(?m)^ValidatorKMSEnv = ".*"`, `ValidatorKMSEnv = "NHB_VALIDATOR_RAW_KEY"`)
	if o.peer != "" {
		s = setLine(t, s, `(?m)^  Bootnodes = \[.*\]`, fmt.Sprintf(`  Bootnodes = ["%s"]`, o.peer))
		s = setLine(t, s, `(?m)^  PersistentPeers = \[.*\]`, fmt.Sprintf(`  PersistentPeers = ["%s"]`, o.peer))
	}
	for _, key := range []string{"Proposal", "Prevote", "Precommit", "Commit"} {
		s = setLine(t, s, `(?m)^  `+key+`Timeout = ".*"`, fmt.Sprintf(`  %sTimeout = "%s"`, key, o.timeout))
	}
	return s
}

type e2eNode struct {
	env      *e2eEnv
	name     string
	dataDir  string
	cfgPath  string
	logPath  string
	p2p, rpc int
	key      *e2eKey
	cmd      *exec.Cmd
	logFile  *os.File
	done     chan struct{}
}

func (e *e2eEnv) newNode(name string, key *e2eKey, dataDir string, peer *e2eNode, timeout string, template string) *e2eNode {
	t := e.t
	n := &e2eNode{env: e, name: name, key: key, dataDir: dataDir, p2p: freePort(t), rpc: freePort(t)}
	peerAddr := ""
	if peer != nil {
		peerAddr = fmt.Sprintf("127.0.0.1:%d", peer.p2p)
	}
	if template == "" {
		template = filepath.Join(e.repo, "config.toml")
	}
	cfg := renderDevnetConfig(t, template, configOverlay{dataDir: dataDir, genesis: e.genesis, p2p: n.p2p, rpc: n.rpc, peer: peerAddr, timeout: timeout})
	n.cfgPath = filepath.Join(e.dir, name+".config.toml")
	if err := os.WriteFile(n.cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	n.logPath = filepath.Join(e.dir, name+".log")
	e.nodes = append(e.nodes, n)
	return n
}

func (n *e2eNode) rpcURL() string { return fmt.Sprintf("http://127.0.0.1:%d", n.rpc) }

func (n *e2eNode) start() {
	t := n.env.t
	t.Helper()
	logFile, err := os.OpenFile(n.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(n.env.nhb, "--config", n.cfgPath)
	cmd.Dir = filepath.Dir(n.cfgPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(os.Environ(),
		"NHB_ENV=prod",
		"NHB_VALIDATOR_RAW_KEY="+n.key.hex,
		"NHB_RPC_JWT_SECRET="+n.env.secret,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", n.name, err)
	}
	n.cmd, n.logFile, n.done = cmd, logFile, make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(n.done)
	}()
	t.Logf("started %s (pid %d): p2p 127.0.0.1:%d, rpc 127.0.0.1:%d, data %s", n.name, cmd.Process.Pid, n.p2p, n.rpc, n.dataDir)
}

// kill stops the node the hard way, as a crash or power cut would: it never
// gets to flush or close anything.
func (n *e2eNode) kill() {
	if n.cmd == nil || n.cmd.Process == nil {
		return
	}
	_ = n.cmd.Process.Kill()
	<-n.done
	_ = n.logFile.Close()
	n.cmd = nil
}

func (e *e2eEnv) stopAll() {
	for _, n := range e.nodes {
		n.kill()
	}
}

func (n *e2eNode) running() bool {
	if n.cmd == nil {
		return false
	}
	select {
	case <-n.done:
		return false
	default:
		return true
	}
}

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (n *e2eNode) call(method string, params ...any) (json.RawMessage, error) {
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.rpcURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var reply rpcReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if reply.Error != nil {
		return nil, fmt.Errorf("%s: %s", method, reply.Error.Message)
	}
	return reply.Result, nil
}

// tip reads the node's newest block over RPC.
func (n *e2eNode) tip() (*nodeStatus, error) {
	client := newRPCClient(n.rpcURL())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.latestBlock(ctx)
}

func (n *e2eNode) height() uint64 {
	status, err := n.tip()
	if err != nil {
		return 0
	}
	return status.Height
}

func (n *e2eNode) validators() []string {
	raw, err := n.call("nhb_getValidatorSet")
	if err != nil {
		return nil
	}
	var out struct {
		Validators []struct {
			Address string `json:"address"`
		} `json:"validators"`
	}
	_ = json.Unmarshal(raw, &out)
	var addrs []string
	for _, v := range out.Validators {
		addrs = append(addrs, strings.ToLower(v.Address))
	}
	return addrs
}

func (n *e2eNode) info() *netInfo {
	client := newRPCClient(n.rpcURL())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := client.netInfo(ctx)
	if err != nil {
		return nil
	}
	return info
}

func (n *e2eNode) logTail(lines int) string {
	raw, _ := os.ReadFile(n.logPath)
	parts := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func (n *e2eNode) logCount(substr string) int {
	raw, _ := os.ReadFile(n.logPath)
	return strings.Count(string(raw), substr)
}

// waitUntil polls cond until it holds or the timeout passes, and fails the
// test with what the node logged last when it does not.
func (e *e2eEnv) waitUntil(what string, timeout time.Duration, node *e2eNode, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	tail := ""
	if node != nil {
		tail = "\n--- last log lines of " + node.name + " ---\n" + node.logTail(25)
	}
	e.t.Fatalf("timed out after %s waiting for %s%s", timeout, what, tail)
}

func (e *e2eEnv) mintToken() {
	cmd := exec.Command(e.cli, "rpc-token", "--secret-stdin", "--ttl", "12h")
	cmd.Stdin = strings.NewReader(e.secret)
	out, err := cmd.Output()
	if err != nil {
		e.t.Fatalf("mint rpc token: %v", err)
	}
	e.token = strings.TrimSpace(string(out))
}

// runCLI runs nhb-cli against a node.
func (e *e2eEnv) runCLI(n *e2eNode, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.cli, args...)
	cmd.Env = append(os.Environ(), "RPC_URL="+n.rpcURL(), "NHB_RPC_TOKEN="+e.token)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// traffic sends a steady mix of the transaction kinds a live network carries
// (NHB and ZNHB transfers, heartbeats, stake changes) to node until stop is
// called. Errors are expected while the node restarts and are ignored.
type trafficDriver struct {
	stop func() int
}

func (e *e2eEnv) startTraffic(node *e2eNode) *trafficDriver {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var mu sync.Mutex
	sent := 0
	alice, bob := e.keys["alice"], e.keys["bob"]
	run := func(args ...string) {
		cmd := exec.CommandContext(ctx, e.cli, args...)
		cmd.Env = append(os.Environ(), "RPC_URL="+node.rpcURL(), "NHB_RPC_TOKEN="+e.token)
		if out, err := cmd.CombinedOutput(); err == nil && !strings.Contains(string(out), "Error") {
			mu.Lock()
			sent++
			mu.Unlock()
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for ctx.Err() == nil {
			i++
			switch i % 4 {
			case 0:
				run("send-nhb", bob.addr, fmt.Sprintf("100000000000000%03d", i%1000), alice.path)
			case 1:
				run("send-znhb", alice.addr, fmt.Sprintf("100000000000000%03d", i%1000), bob.path)
			case 2:
				run("heartbeat", alice.path)
			case 3:
				if i%16 == 3 {
					run("stake", "1000000000000000000", bob.path)
				} else {
					run("heartbeat", bob.path)
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(700 * time.Millisecond):
			}
		}
	}()
	return &trafficDriver{stop: func() int {
		cancel()
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		return sent
	}}
}
