package workflow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

// clientSettings holds the completed client construction state. Codec and bus
// implementations stay shared; copying this record detaches the root draft.
type clientSettings struct {
	// Telemetry observes client calls; nil follows the current process default.
	Telemetry *telemetry.Telemetry
	// Codec encodes payloads. Nil uses [JSON]. It must match the codec of
	// the worker's [Registry].
	Codec Codec

	// Events receives [RunStarted], [RunSignaled], and [RunCanceled]. The
	// default, nil, publishes nothing.
	Events *event.Bus
}

// ClientBuilder edits client-only settings, not workflow definitions or starts.
// NewClient copies the final draft while keeping codecs and event buses shared.
//
//	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
//		client.Codec(workflow.JSON{})
//	})
type ClientBuilder struct {
	// config contains the shared codec and bus until publication.
	config clientSettings
}

// Telemetry selects client-call observation and start-carrier injection. It
// does not configure workers in this or another process. Nil follows the
// process default on each call. The service remains shared after publication.
// Panics still propagate unchanged; an interrupted call closes its started
// observation as workflow.panic without recording the panic value or type.
//
//	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
//		client.Telemetry(observations)
//	})
func (client *ClientBuilder) Telemetry(value *telemetry.Telemetry) *ClientBuilder {
	client.config.Telemetry = value

	return client
}

// Codec replaces the payload codec. A final nil selects JSON at construction;
// use the same codec for the worker Registry. It returns the same draft.
//
//	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
//		client.Codec(workflow.JSON{})
//	})
func (client *ClientBuilder) Codec(value Codec) *ClientBuilder {
	client.config.Codec = value

	return client
}

// Events selects the shared bus for run outcomes. Nil publishes nothing;
// it does not affect per-start metadata. It returns the same draft.
//
//	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
//		client.Events(app.Events())
//	})
func (client *ClientBuilder) Events(value *event.Bus) *ClientBuilder {
	client.config.Events = value

	return client
}

// StartOption changes one [StartOptions] setting of [Client.Start].
// Options run in call order before trace injection and do not reconfigure the
// client. Custom options must not retain the per-call settings pointer.
//
//	var option workflow.StartOption = workflow.Delay(time.Minute)
//	run, err := client.Start(ctx, Onboard, "user-42", user, option)
//	if err != nil {
//		return err
//	}
//	_ = run.ID()
type StartOption func(*StartOptions)

// Delay postpones the start of the run by d.
// The driver owns scheduling; this does not sleep in Client.Start.
//
//	run, err := client.Start(ctx, Onboard, "user-42", user, workflow.Delay(time.Minute))
//	if err != nil {
//		return err
//	}
//	_ = run.ID()
func Delay(d time.Duration) StartOption {
	return func(options *StartOptions) {
		options.Delay = d
	}
}

// Metadata attaches free-form data to the run.
// Applying the option copies the outer map. Keep sensitive data out of metadata
// that the selected engine exposes in its operational interfaces.
//
//	run, err := client.Start(ctx, Onboard, "user-42", user,
//		workflow.Metadata(map[string]string{"source": "signup"}))
//	if err != nil {
//		return err
//	}
//	_ = run.ID()
func Metadata(data map[string]string) StartOption {
	return func(options *StartOptions) {
		options.Metadata = maps.Clone(data)
	}
}

// Client starts workflows and talks to their runs through a [Driver]. Create
// it with [NewClient]. A Client is safe for concurrent use.
// The driver and codec remain shared; their lifecycle belongs to their owner.
//
//	client := workflow.NewClient(driver)
//	run, err := client.Start(ctx, Onboard, "user-42", user)
//	if err != nil {
//		return err
//	}
//	_ = run.ID()
type Client struct {
	// telemetry observes client calls independently of worker registration.
	telemetry *telemetry.Telemetry
	// driver runs the workflows.
	driver Driver

	// codec encodes payloads.
	codec Codec

	// events receives the run events; nil publishes nothing.
	events *event.Bus
}

// NewClient returns a client using driver without invoking handlers or doing I/O.
// Callbacks run directly once in order on one fresh draft. Nil entries panic;
// expanded nil/empty slices omit edits. Retaining a draft cannot change clients.
// A final nil codec uses JSON; registry compatibility remains the caller's job.
//
//	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
//		client.Codec(workflow.JSON{})
//	})
func NewClient(driver Driver, configure ...ClientBuilderFunc) *Client {
	draft := &ClientBuilder{}

	for _, configure := range configure {
		configure(draft)
	}

	config := draft.config

	if config.Codec == nil {
		config.Codec = JSON{}
	}

	return &Client{
		telemetry: config.Telemetry,
		driver:    driver,
		codec:     config.Codec,
		events:    config.Events,
	}
}

// Driver returns the underlying [Driver].
// Raw operations bypass the client's typed encoding and action observations.
//
//	backend := client.Driver()
//	status, err := backend.Status(ctx, "onboard", "user-42")
//	if err != nil {
//		return err
//	}
//	_ = status
func (client *Client) Driver() Driver {
	return client.driver
}

// DriverAs returns the first driver of type T in the chain of the underlying
// driver. A driver that wraps another exposes it with an Unwrap() Driver
// method.
// The returned value has the original driver's ownership; lookup does no I/O.
//
//	backend, ok := client.DriverAs[*memoryworkflow.Driver]()
//	if ok {
//		_ = backend // The composition owner remains responsible for Stop.
//	}
func (client *Client) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](client.Driver())
}

// Start begins run id of workflow w with input in and returns its handle. When
// the run already exists it returns the handle of the existing run together
// with an error that satisfies [errors.Is] with [ErrAlreadyStarted], so
// callers may ignore that error to start idempotently.
// One observation includes validation and encoding; ErrAlreadyStarted is an
// expected telemetry outcome. The start span is injected into a separate
// carrier, never into application metadata or payload values.
// Invalid declarations fail with Workflow.Err before encoding or driver I/O.
//
//	run, err := client.Start(ctx, Onboard, "user-42", user)
//	if err != nil && !errors.Is(err, workflow.ErrAlreadyStarted) {
//		return err
//	}
//	_ = run.ID()
func (client *Client) Start[In, Out any](ctx context.Context, w *Workflow[In, Out], id string, in In, opts ...StartOption) (_ *Run[Out], err error) {
	service := telemetryService(client.telemetry)
	ctx, observation := beginObservation(ctx, service, "start", telemetry.Client)
	defer finishClientObservation(ctx, observation, &err, endStartObservation)

	if err := w.Err(); err != nil {
		return nil, fmt.Errorf("start %s: %w", w.name, err)
	}

	if !validID(id) {
		return nil, fmt.Errorf("start %s: %w", w.name, ErrInvalidID)
	}

	var options StartOptions

	for _, opt := range opts {
		opt(&options)
	}

	options.Trace = nil

	if service.Enabled() {
		options.Trace = map[string]string{}
		service.Inject(ctx, telemetry.MapCarrier(options.Trace))
	}

	raw, err := EncodeValue(client.codec, in)

	if err != nil {
		return nil, fmt.Errorf("start %s: %w", w.name, err)
	}

	run := client.Run(w, id)

	if err := client.driver.Start(ctx, w.name, id, raw, options); err != nil {
		if errors.Is(err, ErrAlreadyStarted) {
			return run, fmt.Errorf("start %s %s: %w", w.name, id, err)
		}

		return nil, fmt.Errorf("start %s %s: %w", w.name, id, err)
	}

	client.events.Emit(ctx, RunStarted, RunEvent{
		Workflow: w.name,
		RunID:    id,
	})

	return run, nil
}

// Run returns the handle of run id of workflow w without any I/O. Its methods
// fail with [ErrInvalidID] when id is empty or contains control characters.
// Obtaining a handle does not establish that the run exists.
//
//	run := client.Run(Onboard, "user-42")
//	status, err := run.Status(ctx)
//	if err != nil {
//		return err
//	}
//	_ = status
func (client *Client) Run[In, Out any](w *Workflow[In, Out], id string) *Run[Out] {
	return &Run[Out]{
		name:      w.name,
		id:        id,
		driver:    client.driver,
		codec:     client.codec,
		events:    client.events,
		telemetry: client.telemetry,
	}
}

// validID reports whether id can name a run: it is not empty and contains no
// ASCII control characters (U+0000 to U+001F and U+007F), which drivers put
// into URLs, headers, and log lines. Every other byte is allowed, including
// "/", because drivers escape ids where their backend needs it.
func validID(id string) bool {
	return id != "" && !strings.ContainsFunc(id, func(character rune) bool {
		return character < ' ' || character == 0x7f
	})
}

// Signal sends value v of signal s to run id of workflow w. It returns
// [ErrNotFound] when the run does not exist or has ended.
// Telemetry observes delivery acceptance only. A signal does not replace the
// durable run's start parent, and its value is never recorded.
//
//	if err := client.Signal(ctx, Onboard, "user-42", Approved, approval); err != nil {
//		return err
//	}
func (client *Client) Signal[In, Out, T any](ctx context.Context, w *Workflow[In, Out], id string, s Signal[T], v T) (err error) {
	ctx, observation := beginObservation(ctx, client.telemetry, "signal", telemetry.Client)
	defer finishClientObservation(ctx, observation, &err, endObservation)

	if !validID(id) {
		return fmt.Errorf("signal %s: %w", s.name, ErrInvalidID)
	}

	raw, err := EncodeValue(client.codec, v)

	if err != nil {
		return fmt.Errorf("signal %s: %w", s.name, err)
	}

	if err := client.driver.Signal(ctx, w.name, id, s.name, raw); err != nil {
		return fmt.Errorf("signal %s %s %s: %w", w.name, id, s.name, err)
	}

	client.events.Emit(ctx, RunSignaled, RunSignaledEvent{
		Workflow: w.name,
		RunID:    id,
		Signal:   s.name,
	})

	return nil
}

// ClientBuilderFunc edits the fresh construction draft used by [NewClient].
// Callbacks run synchronously once in order; retaining a draft cannot replace
// the published client's settings. A supplied nil callback panics.
//
// Example:
//
//	policy := workflow.ClientBuilderFunc(func(client *workflow.ClientBuilder) {
//		client.Codec(workflow.JSON{})
//	})
//	client := workflow.NewClient(driver, policy)
type ClientBuilderFunc = func(*ClientBuilder)
