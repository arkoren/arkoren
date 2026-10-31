package application

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// NewSignalContext returns a child canceled by an interrupt (SIGINT), SIGTERM,
// parent cancellation, or stop. Use it at the process entry point to request
// graceful shutdown through [Application.Run]. Like [signal.NotifyContext],
// it changes process-wide signal handling until stop releases its registration;
// always call stop, including after parent cancellation or startup failure.
// Stop is safe to call repeatedly and cancels the returned context.
// A nil parent panics, following the standard context convention.
//
// Signal delivery follows the platform's os/signal support. This helper does
// not change terminal settings or erase the terminal's echoed ^C: cancellation
// behaves identically with terminal, redirected, and service-manager output.
//
// Example:
//
//	ctx, stop := application.NewSignalContext(context.Background())
//	defer stop()
//	app := application.New()
//	if err := app.Run(ctx, compose); err != nil {
//		return err
//	}
func NewSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
