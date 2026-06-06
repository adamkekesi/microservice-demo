// Package config provides small, dependency-free helpers for reading
// configuration from environment variables. Each service composes its own
// typed config struct from these helpers (see <service>/cmd/<service>/main.go).
package config

import (
	"os"
	"strconv"
	"time"
)

// String returns the env var or the fallback when unset/empty.
func String(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// MustString returns the env var or panics — used for values with no safe
// default (e.g. DATABASE_URL).
func MustString(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		panic("required environment variable not set: " + key)
	}
	return v
}

// Int returns the env var parsed as int, or the fallback when unset/invalid.
func Int(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// Bool returns the env var parsed as bool, or the fallback when unset/invalid.
func Bool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

// Seconds reads an integer env var as a duration in seconds.
func Seconds(key string, fallbackSeconds int) time.Duration {
	return time.Duration(Int(key, fallbackSeconds)) * time.Second
}

// Millis reads an integer env var as a duration in milliseconds.
func Millis(key string, fallbackMillis int) time.Duration {
	return time.Duration(Int(key, fallbackMillis)) * time.Millisecond
}
