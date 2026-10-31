package schedule

import "context"

// Dispatch runs the entry or task called name. Drivers call it when an entry
// is due or a deferred job becomes due. Entries receive a nil payload. An error
// from Dispatch does not end [Driver.Run]; the driver logs it and may retry
// the job according to its engine.
//
//	dispatch := schedule.Dispatch(func(ctx context.Context, entryName string, payload []byte) error {
//		return execute(ctx, entryName, payload)
//	})
//	if err := driver.Run(ctx, entries, dispatch); err != nil {
//		return err
//	}
type Dispatch = func(ctx context.Context, name string, payload []byte) error

// Driver decides when work runs. The [Scheduler] owns what runs; the driver
// owns timing, persistence, and coordination between processes.
// Execution drain, overlap coordination, and supported zones are driver-specific.
//
//	var driver schedule.Driver = memoryschedule.New()
//	scheduler := schedule.New(driver)
//	compose.Run(scheduler.Run)
type Driver interface {
	// Run calls dispatch whenever an entry in entries is due, and whenever a
	// deferred job becomes due, until ctx is canceled or an error occurs.
	// Cancellation of ctx is not an error: Run then returns nil. Run returns
	// [ErrUnsupported] for an entry the driver cannot schedule, such as one
	// in a time zone it cannot evaluate.
	//
	//	if err := driver.Run(ctx, entries, dispatch); err != nil {
	//		return err
	//	}
	Run(ctx context.Context, entries []Entry, dispatch Dispatch) error

	// Defer stores job for later dispatch and returns its non-empty,
	// distinct identifier. A job is dispatched by a running [Driver.Run],
	// possibly in another process: Defer before Run is accepted, and a job
	// whose time has passed is dispatched as soon as possible. A driver
	// without support returns [ErrUnsupported].
	//
	//	id, err := driver.Defer(ctx, job)
	//	if err != nil {
	//		return err
	//	}
	//	fmt.Println(id)
	Defer(ctx context.Context, job Job) (string, error)

	// Cancel removes the deferred job with the given identifier. An unknown or
	// finished identifier is not an error.
	//
	//	if err := driver.Cancel(ctx, id); err != nil {
	//		return err
	//	}
	Cancel(ctx context.Context, id string) error
}
