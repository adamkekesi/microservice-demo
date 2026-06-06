package observability

import (
	"net"
	"os"
	"time"

	"github.com/DataDog/datadog-go/v5/statsd"
)

// Metrics is a thin, nil-safe wrapper over a DogStatsD client. Domain metrics
// are namespaced under "logistics." (technical plan §5.5). All methods are
// safe to call on a nil *Metrics, so the service layer can be unit-tested
// without a running agent.
type Metrics struct {
	client *statsd.Client
}

// NewMetrics constructs a DogStatsD client pointed at the local agent. It does
// not fail when no agent is present (DogStatsD is fire-and-forget over UDP);
// callers may treat a returned error as non-fatal.
func NewMetrics() (*Metrics, error) {
	host := getenv("DD_AGENT_HOST", "localhost")
	port := getenv("DD_DOGSTATSD_PORT", "8125")
	c, err := statsd.New(
		net.JoinHostPort(host, port),
		statsd.WithTags([]string{
			"service:" + os.Getenv("DD_SERVICE"),
			"env:" + os.Getenv("DD_ENV"),
		}),
	)
	if err != nil {
		return &Metrics{}, err
	}
	return &Metrics{client: c}, nil
}

// Incr increments a counter by one.
func (m *Metrics) Incr(name string, tags ...string) {
	if m == nil || m.client == nil {
		return
	}
	_ = m.client.Incr(name, tags, 1)
}

// Gauge records a point-in-time value.
func (m *Metrics) Gauge(name string, value float64, tags ...string) {
	if m == nil || m.client == nil {
		return
	}
	_ = m.client.Gauge(name, value, tags, 1)
}

// Timing records a duration (e.g. commit latency).
func (m *Metrics) Timing(name string, d time.Duration, tags ...string) {
	if m == nil || m.client == nil {
		return
	}
	_ = m.client.Timing(name, d, tags, 1)
}

// Close flushes and closes the underlying client.
func (m *Metrics) Close() {
	if m == nil || m.client == nil {
		return
	}
	_ = m.client.Close()
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
