package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/codetrek/d2r-server/internal/control"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("d2r-server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var config control.Config
	flags.StringVar(&config.ListenAddress, "listen", "127.0.0.1:8080", "control HTTP listen address")
	flags.DurationVar(&config.ShutdownTimeout, "shutdown-timeout", 5*time.Second, "maximum time to drain requests after termination")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: d2r-server [-listen ADDRESS] [-shutdown-timeout DURATION]")
		fmt.Fprintln(stderr, "Runs operational endpoints; a multiplayer game worker is not available.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || config.ListenAddress == "" || config.ShutdownTimeout <= 0 {
		flags.Usage()
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	if err := control.Run(ctx, config, logger); err != nil {
		// Cancellation alone is an orderly signal-driven exit; a drain failure
		// joined to that cancellation must still fail the process.
		if isCancellationOnly(err) {
			return 0
		}
		logger.ErrorContext(ctx, "control_server", "component", "control", "phase", "failed", "error", err)
		return 1
	}
	return 0
}

func isCancellationOnly(err error) bool {
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range wrapped.Unwrap() {
			if !isCancellationOnly(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return isCancellationOnly(wrapped.Unwrap())
	default:
		return err == context.Canceled
	}
}
