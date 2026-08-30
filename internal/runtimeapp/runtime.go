package runtimeapp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// NewJSONLogger creates the process logger used by command binaries.
func NewJSONLogger(level string) *slog.Logger {
	var configured slog.Level
	switch level {
	case "debug":
		configured = slog.LevelDebug
	case "warn":
		configured = slog.LevelWarn
	case "error":
		configured = slog.LevelError
	default:
		configured = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: configured}))
}

// ServeHTTP runs an HTTP server until it fails or the context is cancelled.
func ServeHTTP(
	ctx context.Context,
	logger *slog.Logger,
	server *http.Server,
	shutdownTimeout time.Duration,
) error {
	errorChannel := make(chan error, 1)
	go func() {
		logger.InfoContext(ctx, "HTTP server listening", "address", server.Addr)
		errorChannel <- server.ListenAndServe()
	}()

	select {
	case err := <-errorChannel:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		err := <-errorChannel
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
