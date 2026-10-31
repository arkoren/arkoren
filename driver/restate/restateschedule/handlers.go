package restateschedule

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/core/service/schedule"
)

// specInput is the stored and transmitted form of an entry spec.
type specInput struct {
	// Expression contains the cron expression from the published schedule
	// specification.
	Expression string
	// Location names the time zone used to evaluate the cron expression.
	Location string
	// Exclusive disables overlapping runs of this schedule when true.
	Exclusive bool
	// Hash identifies the published schedule settings for reconciliation.
	Hash string
}

// tick is the message that drives one step of an entry's chain.
type tick struct {
	// Gen is the generation of the spec the tick belongs to.
	Gen int
	// At is the scheduled time in Unix milliseconds.
	At int64
}

// runJob is the input of the runner.
type runJob struct {
	// Name selects the application schedule entry passed to dispatch. Recurring
	// ticks and one-shot invocations use the same dispatch name.
	Name string
	// Payload contains the opaque JSON value forwarded to application dispatch.
	// Omission denotes a recurring entry; explicit JSON null remains task data.
	Payload jsontext.Value `json:",omitempty"`
}

// nextAfter returns the first time after the given time that spec matches.
// It rebuilds the core spec, so it follows the core parser exactly.
func nextAfter(input specInput, after time.Time) (time.Time, error) {
	location, err := time.LoadLocation(input.Location)

	if err != nil {
		return time.Time{}, fmt.Errorf("load location %q: %w", input.Location, err)
	}

	spec := schedule.In(location).Cron(input.Expression)

	if err := spec.Err(); err != nil {
		return time.Time{}, err
	}

	next := spec.Next(after)

	if next.IsZero() {
		return time.Time{}, fmt.Errorf("no run after %s: %w", after, schedule.ErrInvalidEntry)
	}

	return next, nil
}

// tickKey returns the idempotency key of a tick.
func tickKey(name string, gen int, at int64) string {
	return fmt.Sprintf("%s:%d:%d", name, gen, at)
}

// definitions returns the three services of the driver.
func (driver *Driver) definitions() []restatesdk.ServiceDefinition {
	var timeouts []restatesdk.HandlerOption

	if driver.config.MaxRunTime > 0 {
		timeouts = []restatesdk.HandlerOption{
			restatesdk.WithAbortTimeout(driver.config.MaxRunTime),
			restatesdk.WithInactivityTimeout(driver.config.MaxRunTime),
		}
	}

	object := restatesdk.NewObject(driver.names.schedule).
		Handler("Ensure", restatesdk.NewObjectHandler(driver.ensure)).
		Handler("Tick", restatesdk.NewObjectHandler(driver.tick, timeouts...)).
		Handler("Remove", restatesdk.NewObjectHandler(driver.remove))
	index := restatesdk.NewObject(driver.names.index).
		Handler("Sync", restatesdk.NewObjectHandler(driver.sync))
	runner := restatesdk.NewService(driver.names.runner).
		Handler("Run", restatesdk.NewServiceHandler(driver.run, timeouts...))

	return []restatesdk.ServiceDefinition{object, index, runner}
}

// now returns a durable current time.
func now(ctx restatesdk.Context) (time.Time, restatesdk.TerminalError) {
	millis, err := restatesdk.Run(ctx, func(restatesdk.RunContext) (int64, error) {
		return time.Now().UnixMilli(), nil
	})

	if err != nil {
		return time.Time{}, err
	}

	return time.UnixMilli(millis), nil
}

// ensure stores a changed spec and starts a new chain for it.
func (driver *Driver) ensure(ctx restatesdk.ObjectContext, spec specInput) (restatesdk.Void, error) {
	var err error
	stored, err := restatesdk.Get[string](ctx, "hash")

	if err != nil {
		return restatesdk.Void{}, err
	}

	if stored == spec.Hash {
		return restatesdk.Void{}, nil
	}

	generation, err := restatesdk.Get[int](ctx, "gen")

	if err != nil {
		return restatesdk.Void{}, err
	}

	generation++

	restatesdk.Set(ctx, "gen", generation)
	restatesdk.Set(ctx, "spec", spec)
	restatesdk.Set(ctx, "hash", spec.Hash)

	current, err := now(ctx)

	if err != nil {
		return restatesdk.Void{}, err
	}

	next, err := nextAfter(spec, current)

	if err != nil {
		driver.config.Logger.ErrorContext(ctx, "restate schedule: no next run", slog.String("name", restatesdk.Key(ctx)), slog.Any("error", err))

		return restatesdk.Void{}, nil
	}

	driver.sendTick(ctx, generation, next, current)

	return restatesdk.Void{}, nil
}

// sendTick sends the tick for next to the object itself.
func (driver *Driver) sendTick(ctx restatesdk.ObjectContext, generation int, next, current time.Time) {
	name := restatesdk.Key(ctx)
	at := next.UnixMilli()

	restatesdk.ObjectSend(ctx, driver.names.schedule, name, "Tick").
		Send(tick{
			Gen: generation,
			At:  at,
		},
			restatesdk.WithDelay(max(next.Sub(current), 0)),
			restatesdk.WithIdempotencyKey(tickKey(name, generation, at)))
}

// tick sends the next tick of the chain, then runs the entry.
func (driver *Driver) tick(ctx restatesdk.ObjectContext, message tick) (restatesdk.Void, error) {
	var err error
	generation, err := restatesdk.Get[int](ctx, "gen")

	if err != nil {
		return restatesdk.Void{}, err
	}

	hash, err := restatesdk.Get[string](ctx, "hash")

	if err != nil {
		return restatesdk.Void{}, err
	}

	if message.Gen != generation || hash == "" {
		return restatesdk.Void{}, nil
	}

	spec, err := restatesdk.Get[specInput](ctx, "spec")

	if err != nil {
		return restatesdk.Void{}, err
	}

	current, err := now(ctx)

	if err != nil {
		return restatesdk.Void{}, err
	}

	name := restatesdk.Key(ctx)
	next, err := nextAfter(spec, time.UnixMilli(max(current.UnixMilli(), message.At)))

	if err != nil {
		driver.config.Logger.ErrorContext(ctx, "restate schedule: no next run", slog.String("name", name), slog.Any("error", err))
	} else {
		driver.sendTick(ctx, generation, next, current)
	}

	if !spec.Exclusive {
		restatesdk.ServiceSend(ctx, driver.names.runner, "Run").Send(runJob{
			Name: name,
		})

		return restatesdk.Void{}, nil
	}

	_, err = restatesdk.Run(ctx, func(run restatesdk.RunContext) (restatesdk.Void, error) {
		return restatesdk.Void{}, driver.invoke(run, name, nil)
	})

	if err != nil {
		driver.config.Logger.ErrorContext(ctx, "restate schedule: run failed", slog.String("name", name), slog.Any("error", err))
	}

	return restatesdk.Void{}, nil
}

// remove clears the active specification but retains its generation tombstone.
// A later Ensure increments that generation, so ticks queued before removal
// cannot become valid again when the same entry name is reintroduced.
func (driver *Driver) remove(ctx restatesdk.ObjectContext, _ restatesdk.Void) (restatesdk.Void, error) {
	restatesdk.Clear(ctx, "hash")
	restatesdk.Clear(ctx, "spec")

	return restatesdk.Void{}, nil
}

// sync removes the entries that disappeared since the last call.
func (driver *Driver) sync(ctx restatesdk.ObjectContext, names []string) (restatesdk.Void, error) {
	stored, err := restatesdk.Get[[]string](ctx, "names")

	if err != nil {
		return restatesdk.Void{}, err
	}

	for _, name := range stored {
		if !slices.Contains(names, name) {
			restatesdk.ObjectSend(ctx, driver.names.schedule, name, "Remove").Send(restatesdk.Void{})
		}
	}

	restatesdk.Set(ctx, "names", names)

	return restatesdk.Void{}, nil
}

// run runs a job or an overlapping entry.
func (driver *Driver) run(ctx restatesdk.Context, job runJob) (restatesdk.Void, error) {
	_, err := restatesdk.Run(ctx, func(run restatesdk.RunContext) (restatesdk.Void, error) {
		return restatesdk.Void{}, driver.invoke(run, job.Name, job.Payload)
	})

	if err != nil {
		return restatesdk.Void{}, err
	}

	return restatesdk.Void{}, nil
}

// invoke calls dispatch. A missing dispatch or an unknown name is a plain
// error, which Restate retries because another deployment may know the name.
// Any other error is terminal.
func (driver *Driver) invoke(ctx context.Context, name string, payload []byte) error {
	dispatch := driver.currentDispatch()

	if dispatch == nil {
		return errors.New("schedule dispatch is not set")
	}

	err := dispatch(ctx, name, payload)

	switch {
	case err == nil:
		return nil
	case errors.Is(err, schedule.ErrUnknownName):
		return err
	default:
		return restatesdk.TerminalErrorf("run %s: %v", name, err)
	}
}
