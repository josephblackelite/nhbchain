package observability

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsAddrEnv names the environment variable that enables the validator's
// Prometheus listener. It is empty (disabled) by default: the validator
// binaries used to expose no /metrics route at all, so every gauge registered
// in this package (block interval, the liveness and containment metrics) was
// invisible to Prometheus. The endpoint is unauthenticated and meant for a
// loopback or otherwise private bind.
const MetricsAddrEnv = "NHB_METRICS_ADDR"

// StartMetricsServer serves the default Prometheus registry at /metrics on
// addr and returns the running server; an empty addr returns (nil, nil) and
// serves nothing. The listener is bound before this returns so a bad address
// is reported to the caller instead of failing in a goroutine. It does not
// stop the process on failure: an observability problem must never take a
// validator down, so callers log the error and carry on.
func StartMetricsServer(addr string, logger *slog.Logger) (*http.Server, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if !isLoopbackAddr(listener.Addr()) {
		logger.Warn("metrics endpoint is bound to a non-loopback address and is unauthenticated",
			slog.String("addr", listener.Addr().String()))
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("metrics endpoint stopped", slog.Any("error", serveErr))
		}
	}()
	logger.Info("metrics endpoint listening", slog.String("addr", listener.Addr().String()))
	return server, nil
}

// StartMetricsServerFromEnv starts the metrics listener when NHB_METRICS_ADDR
// is set. Failure is logged, never fatal.
func StartMetricsServerFromEnv(logger *slog.Logger) *http.Server {
	addr := os.Getenv(MetricsAddrEnv)
	if strings.TrimSpace(addr) == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	server, err := StartMetricsServer(addr, logger)
	if err != nil {
		logger.Error("failed to start metrics endpoint; continuing without it",
			slog.String("env", MetricsAddrEnv), slog.Any("error", err))
		return nil
	}
	return server
}

func isLoopbackAddr(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp == nil {
		return false
	}
	return tcp.IP.IsLoopback()
}
