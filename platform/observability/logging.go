package observability

import (
	"context"
	"os"

	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewLogger builds a production JSON zap logger at the level given by
// LOG_LEVEL (default "info").
func NewLogger() *zap.Logger {
	level := zapcore.InfoLevel
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		_ = level.UnmarshalText([]byte(v))
	}
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(level)
	cfg.EncoderConfig.TimeKey = "timestamp"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	l, err := cfg.Build()
	if err != nil {
		return zap.NewNop()
	}
	return l
}

// WithTrace enriches a logger with Datadog correlation fields drawn from the
// active span in ctx, so log lines link to traces in the APM UI (plan §5.6).
func WithTrace(ctx context.Context, l *zap.Logger) *zap.Logger {
	if span, ok := tracer.SpanFromContext(ctx); ok {
		sctx := span.Context()
		l = l.With(
			zap.String("dd.trace_id", sctx.TraceID()), // 128-bit hex string in v2
			zap.Uint64("dd.span_id", sctx.SpanID()),
			zap.String("dd.service", os.Getenv("DD_SERVICE")),
			zap.String("dd.env", os.Getenv("DD_ENV")),
			zap.String("dd.version", os.Getenv("DD_VERSION")),
		)
	}
	return l
}

type loggerKey struct{}

// ContextWithLogger stores a request-scoped logger on the context so the
// service layer (which has no Gin/HTTP types) can retrieve it.
func ContextWithLogger(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// LoggerFrom returns the request-scoped logger, or a no-op logger if absent.
func LoggerFrom(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*zap.Logger); ok && l != nil {
		return l
	}
	return zap.NewNop()
}

// RequestLogger is Gin middleware that derives a trace-correlated logger for
// each request and stores it on the request context for handlers/services.
func RequestLogger(base *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		l := WithTrace(ctx, base)
		c.Request = c.Request.WithContext(ContextWithLogger(ctx, l))
		c.Next()
	}
}
