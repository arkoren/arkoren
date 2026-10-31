// Package gcpcaptcha verifies website tokens with Google's current reCAPTCHA
// CreateAssessment API, not legacy Siteverify. Verify validates token, hostname
// and action; applications must separately interpret the score. Middleware
// requires an explicit minimum score and fails closed.
//
// Example:
//
//	v, err := gcpcaptcha.New(ctx, "shop-prod", "site-key", []string{"shop.example"},
//		func(access *gcpcaptcha.Builder) {
//			access.APIKey(apiKey)
//		})
//	if err != nil {
//		return err
//	}
//	assessment, err := v.Verify(ctx, gcpcaptcha.Input{Token: token, ExpectedAction: "login"})
//	if err != nil {
//		return err
//	}
//	if assessment.Score < 0.7 {
//		return gcpcaptcha.ErrRejected
//	}
//
// Tokens expire after two minutes and are single use. No request is retried;
// unknown outcomes require a fresh browser token. No lifecycle methods or
// application-owned client cleanup are needed. See New for authentication and
// transport ownership. This package does not implement other Fraud Defense APIs.
package gcpcaptcha
