// Package observability wires the three telemetry pillars (traces, metrics,
// logs) plus profiling through Datadog's dd-trace-go v2 SDK. It is the shared
// foundation every service imports; see the technical plan §5.
package observability

import (
	"os"

	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
)

// InitTracer starts the global Datadog tracer. The tracer auto-reads the
// standard Datadog env vars (DD_ENV, DD_SERVICE, DD_VERSION, DD_AGENT_HOST,
// DD_TRACE_AGENT_PORT); the explicit options below are defensive so behaviour
// is stable even if a var is unset. Call StopTracer (defer) on shutdown.
func InitTracer() {
	// Best-effort: a failed tracer start must not stop the service from booting.
	_ = tracer.Start(
		tracer.WithService(os.Getenv("DD_SERVICE")),
		tracer.WithEnv(os.Getenv("DD_ENV")),
		tracer.WithServiceVersion(os.Getenv("DD_VERSION")),
	)
}

// StopTracer flushes and stops the tracer. Safe to call once during shutdown.
func StopTracer() {
	tracer.Stop()
}
