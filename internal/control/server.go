// Package control provides operational endpoints without simulating a game worker.
package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type Config struct {
	ListenAddress   string
	ShutdownTimeout time.Duration
}

// Run owns its listener and serving goroutine. A canceled context is retained in
// the returned error even when all in-flight requests drain successfully.
func Run(ctx context.Context, config Config, logger *slog.Logger) error {
	if config.ListenAddress == "" {
		return fmt.Errorf("listen address must not be empty")
	}
	if config.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown timeout must be positive")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start control server: %w", err)
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for control HTTP: %w", err)
	}
	return serve(ctx, listener, config.ShutdownTimeout, logger, Handler(logger))
}

func serverFor(handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
		ErrorLog: slog.NewLogLogger(logger.With("component", "control", "phase", "http_error").Handler(), slog.LevelError),
	}
}

func serve(ctx context.Context, listener net.Listener, shutdownTimeout time.Duration, logger *slog.Logger, handler http.Handler) error {
	server := serverFor(handler, logger)
	server.BaseContext = func(net.Listener) context.Context { return context.WithoutCancel(ctx) }
	completed := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		completed <- err
	}()
	logger.InfoContext(ctx, "control_server", "component", "control", "phase", "listening", "address", listener.Addr().String())
	select {
	case err := <-completed:
		return errors.Join(wrapError("serve control HTTP", err), wrapError("close control HTTP", server.Close()))
	case <-ctx.Done():
		logger.InfoContext(ctx, "control_server", "component", "control", "phase", "draining")
		// Preserve context values while granting draining requests their own deadline
		// after the process context has received a termination signal.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		var closeErr error
		if shutdownErr != nil {
			closeErr = server.Close()
		}
		serveErr := <-completed
		logger.InfoContext(ctx, "control_server", "component", "control", "phase", "stopped")
		return errors.Join(ctx.Err(), wrapError("drain control HTTP", shutdownErr), wrapError("force-close control HTTP", closeErr), wrapError("serve control HTTP", serveErr))
	}
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
