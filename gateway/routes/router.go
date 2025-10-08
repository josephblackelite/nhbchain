package routes

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"nhbchain/gateway/middleware"
)

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
		r.Handle("/rpc", cfg.CompatHandler)
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
			sr.Handle("/*", proxy)
			sr.Handle("/", proxy)
		})
	}

	if obs != nil {
		r.Handle("/metrics", obs.MetricsHandler())
	}

	return r, nil
}
