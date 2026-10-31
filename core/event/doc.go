// Package event provides a typed in-process event bus: topics fix payload types, listeners match by
// name or pattern, and dispatch is synchronous.
//
// A [Topic] names one kind of event and fixes its payload type. [Bus.Listen]
// registers a typed listener and [Bus.Dispatch] invokes the listeners of a
// topic in registration order, stopping at the first error; exact listeners
// run before [Bus.ListenRegex] listeners. Event names are dot-separated
// lowercase tokens such as "user.created"; [Bus.Listen] and [Bus.Dispatch]
// return [ErrInvalidName] for any other name.
//
//	var UserCreated = event.NewTopic[User]("user.created")
//
//	bus := event.New()
//	unsubscribe, err := bus.Listen(UserCreated, func(ctx context.Context, user User) error {
//		return sendWelcome(ctx, user)
//	})
//
// The application owns one bus ([arkoren.dev/core/application.Application.Events]).
// Arkoren components take it with their builder's Events setting.
// They publish topics with [Bus.Emit]
// and publish nothing without
// it. Emit logs listener errors and panics to [Builder.Logger] instead of
// returning them, so a
// listener never changes the outcome of the operation it observes. Listeners
// run synchronously on the caller's goroutine with its context: keep them
// short, and hand slow work to a queue. Free functions such as
// auth.SessionIdentity.Login publish nothing; their caller emits its own events.
//
// A signal is a topic without data: [NewSignal] declares it and
// [Bus.Signal] publishes it. Event signals are unrelated to workflow signals,
// which are messages sent to a running workflow.
//
// Payloads never carry secrets, cache or storage contents, tokens, session
// IDs, request bodies, or full URLs. Errors travel as the values the caller
// of the operation receives.
//
// # Construction
//
// New applies callbacks directly once, synchronously in order on one fresh
// Builder. Settings return the same draft; publication copies the root, not
// the logger. Retained drafts cannot change a bus. Omitted callbacks and
// nil/empty expanded slices make no edits; supplied nil entries and callback
// panics propagate. A nil logger resolves slog.Default when a failure is logged.
//
//	events := event.New(func(events *event.Builder) {
//		events.Logger(slog.Default())
//	})
package event
