package restateschedule

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/driver/restate"
)

// Driver is a [schedule.Driver] backed by Restate.
// New binds services before the shared host starts; Run installs application
// dispatch separately. Missing dispatch retries natively instead of consuming
// work before the scheduler runner is ready.
//
// Example:
//
//	driver, err := New(restate.New())
//	if err != nil {
//		panic(err)
//	}
//	_ = schedule.New(driver)
type Driver struct {
	// config is the configuration with defaults applied.
	config settings

	// host is the shared Restate host.
	host *restate.Host

	// gateway reaches the ingress and admin API.
	gateway gateway

	// names holds the Restate service names.
	names serviceNames

	// mu guards dispatch.
	mu sync.Mutex

	// dispatch is the function set by Run.
	dispatch schedule.Dispatch
}

// serviceNames holds the Restate service names of one prefix.
type serviceNames struct {
	// schedule names the virtual-object service that retains each schedule's state.
	schedule string

	// index names the virtual-object service that tracks the declared schedule set.
	index string

	// runner names the stateless service that dispatches each scheduled job.
	runner string
}

// gateway sends messages to Restate from outside a handler.
type gateway interface {
	// sendObject sends a message to a virtual object handler.
	sendObject(ctx context.Context, service, key, handler string, input any, options sendOptions) (string, error)

	// sendService sends a message to a service handler.
	sendService(ctx context.Context, service, handler string, input any, options sendOptions) (string, error)

	// cancel cancels the invocation with the given identifier.
	cancel(ctx context.Context, id string) error
}

// sendOptions are the optional settings of a send.
type sendOptions struct {
	// delay postpones the invocation.
	delay time.Duration

	// idempotencyKey deduplicates the send. Empty sets none.
	idempotencyKey string
}

// New binds the driver's services to host. It returns [restate.ErrStarted]
// when host is already started.
// Callbacks run once in order on one fresh [Builder]; nil entries panic.
// Published settings are independent of a retained draft; a nil host is an error.
//
// Example:
//
//	driver, err := New(restate.New(), func(scheduler *Builder) {
//		scheduler.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func New(host *restate.Host, configure ...BuilderFunc) (*Driver, error) {
	if host == nil {
		return nil, errors.New("new restate schedule: nil host")
	}

	config := buildSettings(configure...)
	config.Prefix = cmp.Or(config.Prefix, "arkoren")
	config.MaxRunTime = cmp.Or(config.MaxRunTime, time.Hour)
	config.Logger = cmp.Or(config.Logger, slog.Default())

	driver := &Driver{
		config: config,
		host:   host,
		gateway: hostGateway{
			host: host,
		},
		names: newServiceNames(config.Prefix),
	}

	for _, definition := range driver.definitions() {
		if err := host.Bind(definition); err != nil {
			return nil, fmt.Errorf("bind %s: %w", definition.Name(), err)
		}
	}

	return driver, nil
}

// newServiceNames builds the service names for prefix.
func newServiceNames(prefix string) serviceNames {
	var builder strings.Builder
	upper := true

	for _, char := range prefix {
		switch {
		case !unicode.IsLetter(char) && !unicode.IsDigit(char):
			upper = true
		case upper:
			builder.WriteRune(unicode.ToUpper(char))
			upper = false
		default:
			builder.WriteRune(char)
		}
	}

	base := builder.String()

	return serviceNames{
		schedule: base + "Schedule",
		index:    base + "ScheduleIndex",
		runner:   base + "Runner",
	}
}

// specHash returns a stable hash of the parts of an entry that decide when it
// runs.
func specHash(expression, location string, exclusive bool) string {
	sum := sha256.Sum256([]byte(expression + "\x00" + location + "\x00" + strconv.FormatBool(exclusive)))

	return hex.EncodeToString(sum[:])
}

// Run stores dispatch, publishes entries to Restate, and blocks until ctx is
// done, including cancellation while publishing. Ingress failures retry until
// cancellation; only invalid or unsupported declarations fail Run.
// It returns [schedule.ErrUnsupported] for a spec in the Local zone.
//
// Example:
//
//	driver, err := New(restate.New())
//	if err != nil {
//		panic(err)
//	}
//	ctx, cancel := context.WithCancel(context.Background())
//	cancel()
//	if err := driver.Run(ctx, nil, nil); err != nil {
//		panic(err)
//	}
func (driver *Driver) Run(ctx context.Context, entries []schedule.Entry, dispatch schedule.Dispatch) error {
	inputs := make(map[string]specInput, len(entries))

	for _, entry := range entries {
		if err := entry.Err(); err != nil {
			return fmt.Errorf("restate schedule %s: %w", entry.Name(), err)
		}

		spec := entry.Spec()
		location := spec.Location().String()

		if location == "Local" {
			return fmt.Errorf("restate schedule %s: local time zone: %w", entry.Name(), schedule.ErrUnsupported)
		}

		exclusive := !entry.Overlapping()
		inputs[entry.Name()] = specInput{
			Expression: spec.Expression(),
			Location:   location,
			Exclusive:  exclusive,
			Hash:       specHash(spec.Expression(), location, exclusive),
		}
	}

	driver.mu.Lock()
	driver.dispatch = dispatch

	if dispatch != nil {
		// Older deployments emitted Payload:null for recurring messages. The
		// declared entry set distinguishes them from actual JSON-null tasks.
		driver.dispatch = func(ctx context.Context, name string, payload []byte) error {
			if _, entry := inputs[name]; entry {
				payload = nil
			}

			return dispatch(ctx, name, payload)
		}
	}

	driver.mu.Unlock()

	names := make([]string, 0, len(inputs))

	for name := range inputs {
		names = append(names, name)
	}

	slices.Sort(names)

	driver.publish(ctx, driver.names.index, driver.config.Prefix, "Sync", names, sendOptions{})

	for _, name := range names {
		input := inputs[name]

		driver.publish(ctx, driver.names.schedule, name, "Ensure", input, sendOptions{})
	}

	<-ctx.Done()

	return nil
}

// publish sends to an object handler, retrying every second until ctx is done.
// The server may be starting, or the deployment may not be registered yet.
func (driver *Driver) publish(ctx context.Context, service, key, handler string, input any, options sendOptions) {
	for {
		_, err := driver.gateway.sendObject(ctx, service, key, handler, input, options)

		if err == nil {
			return
		}

		driver.config.Logger.WarnContext(ctx, "restate schedule: publish failed, retrying", slog.Any("error", err))

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// Defer sends job to the runner with a delay and returns the invocation
// identifier.
// Payload is opaque JSON, including explicit null. The ingress must know the
// deployment; a missing local dispatch does not prevent acceptance and is retried
// when the invocation executes. The returned ID belongs to Cancel, not an entry.
//
// Example:
//
//	id, err := driver.Defer(ctx, schedule.Job{
//		Name: "remind", Payload: []byte(`null`), At: when,
//	})
//	if err != nil {
//		return err
//	}
//	_ = id
func (driver *Driver) Defer(ctx context.Context, job schedule.Job) (string, error) {
	id, err := driver.gateway.sendService(ctx, driver.names.runner, "Run",
		runJob{
			Name:    job.Name,
			Payload: job.Payload,
		},
		sendOptions{
			delay: max(time.Until(job.At), 0),
		})

	if err != nil {
		return "", fmt.Errorf("restate defer %s: %w", job.Name, err)
	}

	return id, nil
}

// Cancel cancels a deferred job. An unknown or finished job is not an error.
// It requests native invocation cancellation; it does not forcibly terminate
// arbitrary application code that has already started running.
//
// Example:
//
//	if err := driver.Cancel(ctx, deferredID); err != nil {
//		return err
//	}
func (driver *Driver) Cancel(ctx context.Context, id string) error {
	if err := driver.gateway.cancel(ctx, id); err != nil {
		return fmt.Errorf("restate cancel %s: %w", id, err)
	}

	return nil
}

// currentDispatch returns the dispatch function set by Run, or nil.
func (driver *Driver) currentDispatch() schedule.Dispatch {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	return driver.dispatch
}
