// Package lease owns managed Vault token renewal and its native watcher workers.
package lease

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hashicorp/vault/api"
)

// Watcher renews one login token. [*api.LifetimeWatcher] implements it.
type Watcher interface {
	// Start renews the token and blocks until renewal ends or Stop is called.
	Start()

	// Stop ends renewal; calling it after renewal ended is harmless.
	Stop()

	// DoneCh receives once when renewal ends: nil at maximum TTL, otherwise
	// the native renewal failure.
	DoneCh() <-chan error
}

// Manager retains cancellation and completion ownership until native renewal
// returns. Its caller serializes Start and Stop; the worker uses only immutable
// callbacks and policy. A timed-out Stop never discards a running worker.
type Manager struct {
	// login authenticates the shared native client.
	login func(context.Context) (*api.Secret, error)

	// newWatcher creates a native renewer for each successful login.
	newWatcher func(*api.Secret) (Watcher, error)

	// logger receives stages and delays, never native diagnostics.
	logger *slog.Logger

	// retryMin and retryMax bound the exponential re-login delay.
	retryMin, retryMax time.Duration

	// stopRenewal cancels the worker; nil means no renewal is owned.
	stopRenewal context.CancelFunc

	// renewed closes only after the worker and its native watcher return.
	renewed chan struct{}
}

// New captures shared native callbacks and the completed renewal policy without
// logging in or starting a worker. The caller supplies a non-nil logger.
func New(
	login func(context.Context) (*api.Secret, error),
	newWatcher func(*api.Secret) (Watcher, error),
	logger *slog.Logger,
	retryMin, retryMax time.Duration,
) *Manager {
	return &Manager{
		login:      login,
		newWatcher: newWatcher,
		logger:     logger,
		retryMin:   retryMin,
		retryMax:   retryMax,
	}
}

// Start logs in and renews renewable tokens on an independent context. The
// caller guards ready-state idempotence. Outstanding shutdown ownership is
// checked before context cancellation or login, preserving restart precedence.
func (manager *Manager) Start(ctx context.Context) error {
	if err := manager.prepareStart(); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	login, err := manager.login(ctx)

	if err != nil {
		return fmt.Errorf("log in to Vault: %w", err)
	}

	renewable, err := login.TokenIsRenewable()

	if err != nil {
		return fmt.Errorf("inspect Vault token renewal: %w", err)
	}

	if renewable {
		manager.startRenewal(login)
	}

	return nil
}

// prepareStart releases completed handles and rejects replacement of a native
// renewal that is still stopping. The caller holds its lifecycle lock.
func (manager *Manager) prepareStart() error {
	if manager.renewed == nil {
		return nil
	}

	select {
	case <-manager.renewed:
		manager.stopRenewal, manager.renewed = nil, nil

		return nil
	default:
		return errors.New("vault token renewal is still stopping")
	}
}

// Stop always signals cancellation before waiting, even with an expired ctx.
// It bounds only the caller's wait; subsequent calls can join the same worker.
func (manager *Manager) Stop(ctx context.Context) error {
	if manager.stopRenewal == nil {
		return nil
	}

	manager.stopRenewal()

	select {
	case <-manager.renewed:
	case <-ctx.Done():
		return ctx.Err()
	}

	manager.stopRenewal, manager.renewed = nil, nil

	return nil
}

// startRenewal runs renew independently of the initial login context.
func (manager *Manager) startRenewal(login *api.Secret) {
	ctx, cancel := context.WithCancel(context.Background())
	renewed := make(chan struct{})
	manager.stopRenewal, manager.renewed = cancel, renewed

	go func() {
		defer close(renewed)

		manager.renew(ctx, login)
	}()
}

// renew watches successive renewable tokens until cancellation or a login that
// cannot renew. Re-login retries retain the original bounded backoff policy.
func (manager *Manager) renew(ctx context.Context, login *api.Secret) {
	for {
		lifetime, err := manager.newWatcher(login)

		if err == nil {
			err = watchToken(ctx, lifetime)
		}

		if ctx.Err() != nil {
			return
		}

		if err != nil {
			// Native diagnostics may contain credentials or response bodies.
			manager.logger.WarnContext(ctx, "Vault token renewal ended, logging in again")
		}

		if login = manager.relogin(ctx); login == nil {
			return
		}

		renewable, err := login.TokenIsRenewable()

		if err != nil {
			manager.logger.ErrorContext(ctx, "invalid Vault token renewability; automatic renewal stopped")

			return
		}

		if !renewable {
			return
		}
	}
}

// watchToken signals the native watcher before joining its Start goroutine.
// Stop cannot interrupt an in-flight SDK RPC, so the worker remains owned until
// that RPC and watcher return, independently of the caller's shutdown budget.
func watchToken(ctx context.Context, lifetime Watcher) error {
	var workers sync.WaitGroup
	workers.Go(lifetime.Start)
	defer workers.Wait()
	defer lifetime.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-lifetime.DoneCh():
		return err
	}
}

// relogin retries login with a doubling delay until success or cancellation.
func (manager *Manager) relogin(ctx context.Context) *api.Secret {
	delay := manager.retryMin

	for {
		login, err := manager.login(ctx)

		if err == nil {
			return login
		}

		if ctx.Err() != nil {
			return nil
		}

		// Record only the failed stage and bounded policy, never native text.
		manager.logger.ErrorContext(ctx, "log in to Vault again", "retry_in", delay)

		timer := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			timer.Stop()

			return nil
		case <-timer.C:
		}

		delay = min(delay*2, manager.retryMax)
	}
}
