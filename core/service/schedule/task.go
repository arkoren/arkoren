package schedule

import "time"

// Task names one kind of deferred job and fixes the payload type T it
// carries. Create it with [NewTask] and register its handler with
// [Scheduler.Handle].
// Names share the entry namespace. Construction does not validate or register
// a handler; Handle checks the portable identifier and payload type binding.
//
//	task := schedule.NewTask[string]("remind")
//	if err := scheduler.Handle(task, remind); err != nil {
//		return err
//	}
type Task[T any] struct {
	// name is the stable application identifier of the task.
	name string
}

// NewTask returns the task called name that carries payloads of type T. The
// name shares one namespace with [Entry] names.
// Validation is deferred to Handle; At requires that handler to be registered.
// Definitions sharing one name must also agree on their payload type.
//
//	task := schedule.NewTask[string]("remind")
//	fmt.Println(task.Name()) // remind
func NewTask[T any](name string) Task[T] {
	return Task[T]{
		name: name,
	}
}

// Name returns the name of the task.
// The zero Task returns empty text and cannot be registered.
//
//	fmt.Println(schedule.NewTask[string]("remind").Name())
func (task Task[T]) Name() string {
	return task.name
}

// Job is a one-off deferred execution handed to [Driver.Defer].
// It is a raw transport record, not a typed task definition. Drivers determine
// persistence and execution; the scheduler facade handles JSON encoding.
//
//	id, err := driver.Defer(ctx, schedule.Job{
//		Name:    "remind",
//		Payload: []byte(`"hello"`),
//		At:      due,
//	})
//	if err != nil {
//		return err
//	}
//	fmt.Println(id)
type Job struct {
	// Name is the name of the registered task to run.
	Name string

	// Payload is the JSON encoding of the task payload.
	Payload []byte

	// At is the time the job becomes due.
	At time.Time
}
