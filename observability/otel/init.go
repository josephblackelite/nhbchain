package otel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// Config captures the knobs for wiring OpenTelemetry exporters.
type Config struct {
	ServiceName string
	Environment string
	Endpoint    string
	Insecure    bool
	Headers     map[string]string
	Metrics     bool
	Traces      bool
}

// Init configures the global OpenTelemetry providers. Callers should invoke the
// returned shutdown function during service teardown.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if cfg.ServiceName == "" {
		return nil, fmt.Errorf("service name required for telemetry")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "localhost:4318"
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(cfg.ServiceName),
	}
	if cfg.Environment != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentKey.String(cfg.Environment))
	}

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attrs...))
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	shutdownFns := make([]func(context.Context) error, 0, 2)

	if cfg.Traces {
		traceOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			traceOpts = append(traceOpts, otlptracehttp.WithHeaders(cfg.Headers))
		}
		traceExporter, err := otlptracehttp.New(ctx, traceOpts...)
		if err != nil {
			return nil, fmt.Errorf("create trace exporter: %w", err)
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(traceExporter,
				sdktrace.WithBatchTimeout(2*time.Second),
				sdktrace.WithMaxExportBatchSize(512),
			),
		)
		otel.SetTracerProvider(tp)
		shutdownFns = append(shutdownFns, tp.Shutdown)
	}

	if cfg.Metrics {
		metricOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			metricOpts = append(metricOpts, otlpmetrichttp.WithHeaders(cfg.Headers))
		}
		metricExporter, err := otlpmetrichttp.New(ctx, metricOpts...)
		if err != nil {
			return nil, fmt.Errorf("create metric exporter: %w", err)
		}
		metricReader := sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(15*time.Second))
		provider := sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(metricReader),
		)
		otel.SetMeterProvider(provider)
		shutdownFns = append(shutdownFns, provider.Shutdown)
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(ctx context.Context) error {
		var shutdownErr error
		for i := len(shutdownFns) - 1; i >= 0; i-- {
			if err := shutdownFns[i](ctx); err != nil && shutdownErr == nil {
				shutdownErr = err
			}
		}
		return shutdownErr
	}, nil
}

// ParseHeaders converts a comma-separated OTEL header string (key=value,foo=bar)
// into a map suitable for the exporter configuration.
func ParseHeaders(raw string) map[string]string {
	headers := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(pair)
		if trimmed == "" {
			continue
		}
		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		headers[key] = value
	}
	return headers
}
