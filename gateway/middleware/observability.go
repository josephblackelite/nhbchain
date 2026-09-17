package middleware

import (
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type ObservabilityConfig struct {
	ServiceName   string
	MetricsPrefix string
	LogRequests   bool
	Enabled       bool
}

type Observability struct {
	cfg       ObservabilityConfig
	logger    *log.Logger
	tracer    trace.Tracer
	requests  *prometheus.CounterVec
	durations *prometheus.HistogramVec
	registry  *prometheus.Registry
}

func NewObservability(cfg ObservabilityConfig, logger *log.Logger) *Observability {
	if logger == nil {
		logger = log.Default()
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "nhb-gateway"
	}
	if cfg.MetricsPrefix == "" {
		cfg.MetricsPrefix = "gateway"
	}
	registry := prometheus.NewRegistry()
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.MetricsPrefix,
		Name:      "requests_total",
		Help:      "Total HTTP requests processed by the gateway.",
	}, []string{"route", "method", "status"})
	durations := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: cfg.MetricsPrefix,
		Name:      "request_duration_seconds",
		Help:      "Duration of HTTP requests in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"route", "method"})
	registry.MustRegister(requests, durations)
	tracer := otel.Tracer(cfg.ServiceName)
	return &Observability{
		cfg:       cfg,
		logger:    logger,
		tracer:    tracer,
		requests:  requests,
		durations: durations,
		registry:  registry,
	}
}

func (o *Observability) Middleware(route string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !o.cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			ctx, span := o.tracer.Start(r.Context(), route, trace.WithAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.route", route),
			))
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r.WithContext(ctx))
			span.SetAttributes(attribute.Int("http.status_code", recorder.status))
			span.End()
			duration := time.Since(start).Seconds()
			o.requests.WithLabelValues(route, r.Method, http.StatusText(recorder.status)).Inc()
			o.durations.WithLabelValues(route, r.Method).Observe(duration)
			if o.cfg.LogRequests {
				o.logger.Printf("%s %s -> %d (%.2fms)", r.Method, r.URL.Path, recorder.status, duration*1000)
			}
		})
	}
}

func (o *Observability) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(o.registry, promhttp.HandlerOpts{})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
