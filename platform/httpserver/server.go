// Package httpserver runs an http.Server with graceful shutdown.
package httpserver

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Run starts an HTTP server on the given port and blocks until SIGINT/SIGTERM.
// On signal it drains in-flight requests (15s budget) and then invokes
// onShutdown (e.g. tracer.Stop / profiler.Stop) — see technical plan §3.
func Run(handler http.Handler, port string, onShutdown func()) error {
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if onShutdown != nil {
			onShutdown()
		}
		return err
	case <-stop:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := srv.Shutdown(ctx)
	if onShutdown != nil {
		onShutdown()
	}
	return err
}
