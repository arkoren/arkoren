// Package pushtest provides an outbound push fake and Driver conformance suite.
// Fake records accepted submissions, including resolved retention, without a
// delivery engine, network access, or lifecycle. It accepts both recipient
// variants after portable validation; real drivers may support only one.
//
//	fake := pushtest.New()
//	sender := push.New(fake)
//	_, err := sender.Send(ctx, push.NewMessage(push.Token("device-registration"), "Ready"))
//	if err != nil {
//		return err
//	}
//	fake.AssertSent(t, func(submission pushtest.Submission) bool {
//		return submission.Message.Title() == "Ready" && submission.Config.TTL == push.DefaultTTL
//	})
//
// Run verifies portable direct-driver behavior with a valid driver-specific
// recipient supplied by each factory. It does not verify delivery, endpoint
// security, native error mapping, or provider retention encoding; those need
// focused driver tests. Factories must arrange successful backend acceptance.
package pushtest
