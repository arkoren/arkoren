package hatchet

import (
	"errors"
	"fmt"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

// Register adds a registration that Start runs after it creates the client.
// It rejects nil with ErrInvalidConfig, acquisition/success with ErrStarted,
// and terminal shutdown with ErrStopped. No callback or I/O runs here.
//
// Example:
//
//	if err := host.Register(registration); err != nil {
//		return err
//	}
func (host *Host) Register(registration Registration) error {
	if registration == nil {
		return fmt.Errorf("register hatchet workflow: %w: nil registration", ErrInvalidConfig)
	}

	host.mutex.Lock()
	defer host.mutex.Unlock()

	if host.stopped {
		return fmt.Errorf("register hatchet workflow: %w", ErrStopped)
	}

	if host.started || host.starting != nil {
		return fmt.Errorf("register hatchet workflow: %w", ErrStarted)
	}

	host.registrations = append(host.registrations, registration)

	return nil
}

// runRegistration calls registration and turns a panic into an error.
func runRegistration(registration Registration, client *hatchet.Client) (workflow hatchet.WorkflowBase, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("registration panicked: %v", recovered)
		}
	}()

	workflow, err = registration(client)

	if err != nil {
		return nil, err
	}

	if workflow == nil {
		return nil, errors.New("registration returned no workflow")
	}

	return workflow, nil
}
