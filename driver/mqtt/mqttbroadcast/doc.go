// Package mqttbroadcast provides MQTT 5 publisher and consumer drivers for the
// broadcast contracts through Eclipse Paho's autopaho connection manager.
//
// [New] defaults QoS to [DefaultMQTTQoS]; [Builder.QoS] sets any
// valid level, including 0. Valid values are 0, 1, and 2.
// Construction callbacks run directly once in order on a fresh Builder before
// validation; nil entries panic and expanded nil slices make no edits. New
// captures URL slices before callbacks. New rejects invalid URLs, QoS, and
// unrepresentable keepalive durations without printing URL credentials. KeepAlive
// is a time.Duration: zero selects 30 seconds, negative disables, and positive
// values must be whole seconds from 1 through 65535. Nil Logger selects
// slog.Default() for handler panics and failed reconnect subscriptions.
//
// New owns a clean, nonpersistent session started by Start. NewFrom borrows a
// manager and applies only QoS and Logger; configure HandlePublish and
// HandleConnectionUp before native construction, then SetClient once before use.
// The native owner must disconnect after the consumer and driver stop. Stop is
// terminal, clears local registrations, and attempts to release borrowed-client
// topic filters without disconnecting the native manager. Do not share those
// filters with unrelated native users on the same manager.
//
// Consume drops malformed envelopes, invalid concrete names, and envelopes whose
// name, with dots replaced by slashes, differs from the actual MQTT topic. Raw
// Subscribe callbacks still receive uninterpreted bytes. Topic routing does not
// authenticate payloads: restrict publishers with broker topic ACLs or separate
// broker namespaces when applications do not trust each other.
// Raw payloads are shared read-only: copy bytes before mutating or retaining them.
// Delivery concurrency is capped at 1024; saturation backpressures the receive
// loop. Default automatic QoS 1 acknowledgement follows callbacks, including
// failures. QoS 2 is a protocol guarantee, not exactly-once application effects.
// Stop's context bounds draining, not arbitrary callback or socket execution;
// borrowed-client cleanup uses a separate ten-second acknowledgement timeout.
// Drivers publish no events; facade telemetry and notifications remain in core.
//
// Typical lifecycle wiring registers cleanup at the acquiring owner before
// startup. This function pattern joins cleanup failures with the run result;
// its caller supplies the operation context and registers typed listeners:
//
//	run := func(ctx context.Context) (err error) {
//		driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"})
//		if err != nil {
//			return err
//		}
//		defer func() {
//			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//			defer cancel()
//			err = errors.Join(err, driver.Stop(cleanup))
//		}()
//		if err := driver.Start(ctx); err != nil {
//			return err
//		}
//		consumer := broadcast.NewConsumer(driver)
//		// Register typed listeners on consumer before starting the runner.
//		return consumer.Run(ctx)
//	}
//	_ = run
package mqttbroadcast
