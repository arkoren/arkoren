// Package broadcasttest tests code that publishes or listens to broadcasts.
// It offers a recording [Fake] with typed assertions and a synchronous
// [Fake.Drain], and [Run], a conformance suite that every
// [broadcast.PublisherDriver] and [broadcast.ConsumerDriver] pair must pass.
//
// A test that exercises code under test publishing broadcasts:
//
//	fake := broadcasttest.New()
//	publisher := broadcast.NewPublisher(fake)
//	consumer := broadcast.NewConsumer(fake)
//	_, _ = consumer.Listen(topic, listener)
//
//	_ = publisher.Publish(ctx, topic, payload)
//	fake.AssertPublished(t, topic, func(p Payload) bool {
//		return p.ID == 1
//	})
//	_ = fake.Drain(ctx, consumer)
//
// Drain runs the real [broadcast.Consumer], so listener matching, decoding,
// and error handling behave as in production.
//
// A driver author proves a driver with the suite:
//
//	func TestConformance(t *testing.T) {
//		broadcasttest.Run(t, func(t *testing.T) broadcasttest.Driver {
//			driver := memorybroadcast.New()
//			t.Cleanup(func() {
//				_ = driver.Stop(context.Background())
//			})
//
//			return broadcasttest.Driver{Publisher: driver, Consumer: driver}
//		})
//	}
package broadcasttest
