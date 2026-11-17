package routes

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"nhbchain/gateway/middleware"
)

// CompatRateLimitKey names the rate limit that applies to the /rpc
// compatibility endpoint.
const CompatRateLimitKey = "compat"

type ServiceRoute struct {
	Name           string
	Prefix         string
	Target         *url.URL
	RequireAuth    bool
	RequiredScopes []string
	RateLimitKey   string
}

type Config struct {
	Routes        []ServiceRoute
	CompatHandler http.Handler
	HealthHandler http.Handler
	Authenticator *middleware.Authenticator
	RateLimiter   *middleware.RateLimiter
	Observability *middleware.Observability
	CORS          middleware.CORSConfig
}

func New(cfg Config) (http.Handler, error) {
	r := chi.NewRouter()
	if cfg.CORS.AllowedOrigins != nil || cfg.CORS.AllowedMethods != nil {
		r.Use(middleware.CORS(cfg.CORS))
	} else {
		r.Use(middleware.CORS(middleware.CORSConfig{}))
	}

	obs := cfg.Observability
	if obs != nil {
		r.Use(obs.Middleware("root"))
	}

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	if cfg.CompatHandler != nil {
		// /rpc goes through the same rate limit and the same authenticator as
		// the routes it stands in for. It used to be mounted ahead of them, so a
		// caller with no token could reach every service behind it; the dispatcher
		// holds each call to its service's scopes (see compat.ScopeGuard).
		r.Group(func(cr chi.Router) {
			if cfg.RateLimiter != nil {
				cr.Use(cfg.RateLimiter.Middleware(CompatRateLimitKey))
			}
			if cfg.Authenticator != nil {
				cr.Use(cfg.Authenticator.Middleware())
			}
			cr.Handle("/rpc", cfg.CompatHandler)
		})
	}

	for _, route := range cfg.Routes {
		proxy := NewProxy(route.Target, route.Prefix)
		var lendingBridge *lendingRoutes
		if route.Name == "lending" {
			lr, err := newLendingRoutes(route.Target)
			if err != nil {
				return nil, fmt.Errorf("configure lending routes: %w", err)
			}
			lendingBridge = lr
		}
		var txBridge *transactionsRoutes
		if route.Name == "transactions" {
			tr, err := newTransactionsRoutes(route.Target)
			if err != nil {
				return nil, fmt.Errorf("configure transaction routes: %w", err)
			}
			txBridge = tr
		}
		var walletBridge *walletRoutes
		if route.Name == "consensus" {
			wr, err := newWalletRoutes(route.Target)
			if err != nil {
				return nil, fmt.Errorf("configure wallet routes: %w", err)
			}
			walletBridge = wr
		}
		r.Route(route.Prefix, func(sr chi.Router) {
			if cfg.RateLimiter != nil && route.RateLimitKey != "" {
				sr.Use(cfg.RateLimiter.Middleware(route.RateLimitKey))
			}
			if cfg.Authenticator != nil && route.RequireAuth {
				sr.Use(cfg.Authenticator.Middleware(route.RequiredScopes...))
			}
			if obs != nil {
				sr.Use(obs.Middleware(route.Name))
			}
			if lendingBridge != nil {
				lendingBridge.mount(sr)
			}
			if txBridge != nil {
				txBridge.mount(sr)
			}
			if walletBridge != nil {
				walletBridge.mount(sr)
			}
			sr.Handle("/*", proxy)
			sr.Handle("/", proxy)
		})
	}

	if obs != nil {
		r.Handle("/metrics", obs.MetricsHandler())
	}

	return r, nil
}
