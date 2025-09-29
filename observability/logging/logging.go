package logging

import (
	"log"
	"log/slog"
	"os"
	"strings"
)

// Setup configures the standard library logger to emit structured JSON and returns
// the underlying slog.Logger for richer logging within the service. All log lines
// include the service name and environment when provided.
func Setup(service, env string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: false,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey {
				return slog.Attr{Key: "timestamp", Value: attr.Value}
			}
			if attr.Key == slog.LevelKey {
				level := strings.ToUpper(attr.Value.String())
				return slog.String("severity", level)
			}
			if attr.Key == slog.MessageKey {
				return slog.Attr{Key: "message", Value: attr.Value}
			}
			return attr
		},
	})

	attrs := []slog.Attr{
		slog.String("service", strings.TrimSpace(service)),
	}
	if env = strings.TrimSpace(env); env != "" {
		attrs = append(attrs, slog.String("env", env))
	}

	withArgs := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		withArgs = append(withArgs, attr)
	}

	base := slog.New(handler).With(withArgs...)
	slog.SetDefault(base)

	// Bridge the standard library logger so existing packages continue to work.
	stdBridge := slog.NewLogLogger(handler.WithAttrs(attrs), slog.LevelInfo)
	stdBridge.SetFlags(0)
	log.SetOutput(stdBridge.Writer())
	log.SetFlags(0)
	log.SetPrefix("")

	return base
}
