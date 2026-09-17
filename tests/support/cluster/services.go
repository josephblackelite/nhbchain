package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ServiceName string

const (
	ServiceConsensus ServiceName = "consensusd"
	ServiceP2P       ServiceName = "p2pd"
	ServiceLending   ServiceName = "lendingd"
	ServiceSwap      ServiceName = "swapd"
	ServiceGov       ServiceName = "governd"
	ServiceGateway   ServiceName = "gateway"
)

type Cluster struct {
	state    *State
	services map[ServiceName]*serviceProcess
	gateway  *gatewayServer
	client   *http.Client
	mu       sync.Mutex
}

type serviceProcess struct {
	name        ServiceName
	addr        string
	handlerFunc func(*State) http.Handler
	server      *http.Server
	listener    net.Listener
	running     bool
	mu          sync.Mutex
}

func New(ctx context.Context) (*Cluster, error) {
	state := newState()
	cluster := &Cluster{
		state:    state,
		services: make(map[ServiceName]*serviceProcess),
		client:   &http.Client{Timeout: 3 * time.Second},
	}

	cluster.services[ServiceConsensus] = newService(ServiceConsensus, consensusHandler)
	cluster.services[ServiceP2P] = newService(ServiceP2P, p2pHandler)
	cluster.services[ServiceLending] = newService(ServiceLending, lendingHandler)
	cluster.services[ServiceSwap] = newService(ServiceSwap, swapHandler)
	cluster.services[ServiceGov] = newService(ServiceGov, func(state *State) http.Handler {
		return govHandler(state, cluster)
	})

	for name, svc := range cluster.services {
		if err := svc.start(ctx, state); err != nil {
			return nil, fmt.Errorf("start %s: %w", name, err)
		}
	}

	endpoints := make(map[ServiceName]*url.URL)
	for name, svc := range cluster.services {
		parsed, err := url.Parse("http://" + svc.addr)
		if err != nil {
			return nil, fmt.Errorf("parse %s addr: %w", name, err)
		}
		endpoints[name] = parsed
	}

	gw, err := newGatewayServer(endpoints)
	if err != nil {
		return nil, fmt.Errorf("start gateway: %w", err)
	}
	cluster.gateway = gw
	cluster.services[ServiceGateway] = gw.process

	return cluster, nil
}

func newService(name ServiceName, build func(*State) http.Handler) *serviceProcess {
	return &serviceProcess{name: name, handlerFunc: build}
}

func (c *Cluster) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []string
	for name, svc := range c.services {
		if err := svc.stop(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf(strings.Join(errs, "; "))
	}
	return nil
}

func (s *serviceProcess) start(ctx context.Context, state *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	handler := s.handlerFunc(state)
	if handler == nil {
		return fmt.Errorf("handler not provided")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.addr = listener.Addr().String()
	srv := &http.Server{Handler: handler}
	s.server = srv
	s.listener = listener
	go func() {
		_ = srv.Serve(listener)
	}()
	if err := waitForHealthy(ctx, "http://"+s.addr); err != nil {
		_ = srv.Close()
		return err
	}
	s.running = true
	return nil
}

func (s *serviceProcess) stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	s.running = false
	return nil
}

func (s *serviceProcess) restart(ctx context.Context, state *State) error {
	if err := s.stop(ctx); err != nil {
		return err
	}
	// ensure listener is closed before reusing address
	if s.listener != nil {
		_ = s.listener.Close()
	}
	// bind to the previous address if possible
	addr := s.addr
	handler := s.handlerFunc(state)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		// fall back to random port
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	}
	s.addr = listener.Addr().String()
	srv := &http.Server{Handler: handler}
	s.server = srv
	s.listener = listener
	go func() {
		_ = srv.Serve(listener)
	}()
	if err := waitForHealthy(ctx, "http://"+s.addr); err != nil {
		_ = srv.Close()
		return err
	}
	s.running = true
	return nil
}

func waitForHealthy(ctx context.Context, base string) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for {
		reqCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/healthz", nil)
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("health check failed: %w", err)
			}
			return fmt.Errorf("health check failed with status %d", resp.StatusCode)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func consensusHandler(state *State) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/applied", func(w http.ResponseWriter, r *http.Request) {
		snapshot := state.consensusState()
		writeJSON(w, http.StatusOK, snapshot)
	})
	mux.HandleFunc("/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ProposalID int    `json:"proposal_id"`
			RequestID  string `json:"request_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		resp, err := state.apply(req.RequestID, req.ProposalID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	return mux
}

func p2pHandler(state *State) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/peers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"peers": []string{"node-a", "node-b"}})
	})
	return mux
}

func lendingHandler(state *State) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/supply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Account   string `json:"account"`
			Amount    int64  `json:"amount"`
			RequestID string `json:"request_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		resp, err := state.supply(req.Account, req.RequestID, req.Amount)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/borrow", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Account   string `json:"account"`
			Amount    int64  `json:"amount"`
			RequestID string `json:"request_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		resp, err := state.borrow(req.Account, req.RequestID, req.Amount)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/repay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Account   string `json:"account"`
			Amount    int64  `json:"amount"`
			RequestID string `json:"request_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		resp, err := state.repay(req.Account, req.RequestID, req.Amount)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/position", func(w http.ResponseWriter, r *http.Request) {
		account := r.URL.Query().Get("account")
		resp := state.position(account)
		writeJSON(w, http.StatusOK, resp)
	})
	return mux
}

func swapHandler(state *State) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/mint", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Account   string `json:"account"`
			Amount    int64  `json:"amount"`
			RequestID string `json:"request_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		resp, err := state.mint(req.Account, req.RequestID, req.Amount)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/redeem", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Account   string `json:"account"`
			Amount    int64  `json:"amount"`
			RequestID string `json:"request_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		resp, err := state.redeem(req.Account, req.RequestID, req.Amount)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("/balance", func(w http.ResponseWriter, r *http.Request) {
		account := r.URL.Query().Get("account")
		resp := state.balance(account)
		writeJSON(w, http.StatusOK, resp)
	})
	return mux
}

func govHandler(state *State, cluster *Cluster) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/proposals", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var req struct {
				Title       string `json:"title"`
				Description string `json:"description"`
				RequestID   string `json:"request_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid payload", http.StatusBadRequest)
				return
			}
			resp, err := state.propose(req.RequestID, req.Title, req.Description)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, resp)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/proposals/", func(w http.ResponseWriter, r *http.Request) {
		trimmed := strings.TrimPrefix(r.URL.Path, "/proposals/")
		parts := strings.Split(trimmed, "/")
		if len(parts) == 0 {
			http.Error(w, "proposal id required", http.StatusBadRequest)
			return
		}
		id, err := strconv.Atoi(parts[0])
		if err != nil {
			http.Error(w, "invalid proposal id", http.StatusBadRequest)
			return
		}
		if len(parts) == 1 {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			resp, err := state.proposalByID(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		switch parts[1] {
		case "vote":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var req struct {
				Voter     string `json:"voter"`
				Option    string `json:"option"`
				RequestID string `json:"request_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid payload", http.StatusBadRequest)
				return
			}
			resp, err := state.vote(req.RequestID, id, req.Voter, req.Option)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, resp)
		case "apply":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var req struct {
				RequestID string `json:"request_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid payload", http.StatusBadRequest)
				return
			}
			applyResp, err := state.apply(req.RequestID, id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			// forward to consensus apply endpoint to simulate broadcast
			if err := cluster.notifyConsensus(applyResp, req.RequestID); err != nil {
				http.Error(w, fmt.Sprintf("consensus apply failed: %v", err), http.StatusBadGateway)
				return
			}
			writeJSON(w, http.StatusOK, applyResp)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func (c *Cluster) notifyConsensus(resp applyResponse, requestID string) error {
	consensus := c.services[ServiceConsensus]
	if consensus == nil {
		return fmt.Errorf("consensus service missing")
	}
	payload := map[string]any{"proposal_id": resp.ProposalID, "request_id": requestID}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, "http://"+consensus.addr+"/apply", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: 2 * time.Second}
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(res.Body)
		return fmt.Errorf("consensus apply status %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

type gatewayServer struct {
	process *serviceProcess
	client  *http.Client
	routes  map[string]ServiceName
	targets map[ServiceName]*url.URL
}

func newGatewayServer(endpoints map[ServiceName]*url.URL) (*gatewayServer, error) {
	routes := map[string]ServiceName{
		"/v1/lending":   ServiceLending,
		"/v1/swap":      ServiceSwap,
		"/v1/gov":       ServiceGov,
		"/v1/consensus": ServiceConsensus,
	}
	gw := &gatewayServer{
		client:  &http.Client{Timeout: 3 * time.Second},
		routes:  routes,
		targets: endpoints,
	}
	process := newService(ServiceGateway, func(state *State) http.Handler {
		return gw.handler()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := process.start(ctx, newState()); err != nil {
		return nil, err
	}
	gw.process = process
	return gw, nil
}

func (g *gatewayServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		targetName, prefix := g.matchRoute(r.URL.Path)
		if targetName == "" {
			http.NotFound(w, r)
			return
		}
		targetURL := g.targets[ServiceName(targetName)]
		if targetURL == nil {
			http.Error(w, "service unavailable", http.StatusBadGateway)
			return
		}
		forwardPath := strings.TrimPrefix(r.URL.Path, prefix)
		if !strings.HasPrefix(forwardPath, "/") {
			forwardPath = "/" + forwardPath
		}
		outURL := *targetURL
		outURL.Path = path.Join(targetURL.Path, forwardPath)
		outURL.RawQuery = r.URL.RawQuery

		var body io.Reader
		if r.Body != nil {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusBadRequest)
				return
			}
			body = bytes.NewReader(data)
			r.Body = io.NopCloser(bytes.NewReader(data))
		}

		req, err := http.NewRequestWithContext(r.Context(), r.Method, outURL.String(), body)
		if err != nil {
			http.Error(w, "forward request", http.StatusBadRequest)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := g.client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("upstream error: %v", err), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			for _, value := range v {
				w.Header().Add(k, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
	return mux
}

func (g *gatewayServer) matchRoute(path string) (string, string) {
	longestPrefix := ""
	var serviceName ServiceName
	for prefix, svc := range g.routes {
		if strings.HasPrefix(path, prefix) && len(prefix) > len(longestPrefix) {
			longestPrefix = prefix
			serviceName = svc
		}
	}
	if longestPrefix == "" {
		return "", ""
	}
	return string(serviceName), longestPrefix
}

func (g *gatewayServer) updateTarget(name ServiceName, addr string) {
	if g == nil {
		return
	}
	parsed, err := url.Parse(addr)
	if err != nil {
		return
	}
	if g.targets == nil {
		g.targets = make(map[ServiceName]*url.URL)
	}
	g.targets[name] = parsed
}

func (c *Cluster) Kill(ctx context.Context, name ServiceName) error {
	svc, ok := c.services[name]
	if !ok {
		return fmt.Errorf("service %s not found", name)
	}
	return svc.stop(ctx)
}

func (c *Cluster) Restart(ctx context.Context, name ServiceName) (string, error) {
	svc, ok := c.services[name]
	if !ok {
		return "", fmt.Errorf("service %s not found", name)
	}
	if err := svc.restart(ctx, c.state); err != nil {
		return "", err
	}
	if name != ServiceGateway && c.gateway != nil {
		c.gateway.updateTarget(name, "http://"+svc.addr)
	}
	return svc.addr, nil
}

func (c *Cluster) GatewayURL() string {
	gw := c.services[ServiceGateway]
	if gw == nil {
		return ""
	}
	return "http://" + gw.addr
}

func (c *Cluster) ServiceURL(name ServiceName) string {
	svc := c.services[name]
	if svc == nil {
		return ""
	}
	return "http://" + svc.addr
}
