package observability

import (
	"github.com/DataDog/dd-trace-go/v2/profiler"
)

// InitProfiler starts the Datadog continuous profiler when DD_PROFILING_ENABLED
// is true and returns a stop func. When disabled it is a no-op (plan §5.7).
func InitProfiler() (stop func()) {
	if !boolEnv("DD_PROFILING_ENABLED") {
		return func() {}
	}
	err := profiler.Start(
		profiler.WithProfileTypes(profiler.CPUProfile, profiler.HeapProfile),
	)
	if err != nil {
		return func() {}
	}
	return profiler.Stop
}

func boolEnv(key string) bool {
	switch getenv(key, "false") {
	case "1", "t", "T", "true", "TRUE", "True":
		return true
	default:
		return false
	}
}
