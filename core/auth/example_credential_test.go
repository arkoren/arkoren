package auth_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
)

// exampleChallenge posts body to handler with the session of r.
func exampleChallenge(handler router.Handler, r *http.Request, body string) error {
	return handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)).WithContext(r.Context()))
}

func ExampleCredential() {
	credential := auth.Credential{
		Method:   auth.MethodOTP,
		Kind:     auth.KindPossession,
		Enrolled: func(context.Context, string) (bool, error) { return true, nil },
	}

	fmt.Println(credential.Method, credential.Kind, credential.Verify == nil)
	// Output: otp possession true
}

func ExampleErrInvalidCredential() {
	fmt.Println(auth.ErrInvalidCredential.Status())
	// Output: 403
}

func ExampleErrUnknownMethod() {
	fmt.Println(auth.ErrUnknownMethod.Status())
	// Output: 422
}

func ExampleErrNoStepUpMethod() {
	fmt.Println(auth.ErrNoStepUpMethod.Status())
	// Output: 403
}

func ExampleSecondFactorChallenge() {
	var challenge auth.SecondFactorChallenge
	challenge.SecondFactor.Methods = []auth.Method{auth.MethodOTP, auth.MethodHardwareKey}

	body, _ := json.Marshal(challenge)

	fmt.Println(string(body))
	// Output: {"second_factor":{"methods":["otp","hwk"]}}
}

func ExampleOutcome() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(exampleOTP)
	})

	outcome, err := guard.Begin(exampleRequest(), "1", examplePassword)

	fmt.Println(outcome.Complete(), outcome.Methods(), err)
	// Output: false [otp] <nil>
}

func ExampleOutcome_Complete() {
	outcome, _ := exampleGuard().Begin(exampleRequest(), "1", examplePassword)

	fmt.Println(outcome.Complete())
	// Output: true
}

func ExampleOutcome_Methods() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(exampleOTP)
	})
	outcome, _ := guard.Begin(exampleRequest(), "1", examplePassword)

	fmt.Println(outcome.Methods())
	// Output: [otp]
}

func ExampleOutcome_Challenge() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(exampleOTP)
	})
	outcome, _ := guard.Begin(exampleRequest(), "1", examplePassword)

	fmt.Println(outcome.Challenge().SecondFactor.Methods)
	// Output: [otp]
}

func ExampleOutcome_Response() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(exampleOTP)
	})
	outcome, _ := guard.Begin(exampleRequest(), "1", examplePassword)
	recorder := httptest.NewRecorder()

	err := outcome.Response(recorder)

	fmt.Println(recorder.Code, strings.TrimSpace(recorder.Body.String()), err)
	// Output: 202 {"second_factor":{"methods":["otp"]}} <nil>
}

func ExamplePendingLogin() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(exampleOTP)
	})
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)

	pending, err := guard.Pending(r)

	fmt.Println(pending.Subject, pending.Methods, err)
	// Output: 1 [otp] <nil>
}

func ExampleGuard_Pending() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(exampleOTP)
	})
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)

	_, first := guard.Pending(r)
	_, second := guard.Pending(r)

	fmt.Println(first, second)
	// Output: <nil> <nil>
}

func ExampleGuard_PendingHandler() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(exampleOTP)
	})
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)
	recorder := httptest.NewRecorder()

	err := guard.PendingHandler()(recorder, r)

	fmt.Println(recorder.Code, strings.TrimSpace(recorder.Body.String()), err)
	// Output: 200 {"methods":["otp"]} <nil>
}

func ExampleGuard_ChallengeHandler() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(exampleOTP)
	})
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)

	wrong := exampleChallenge(guard.ChallengeHandler(), r, `{"method":"otp","code":"bad"}`)
	right := exampleChallenge(guard.ChallengeHandler(), r, `{"method":"otp","code":"ok"}`)
	identity, _ := guard.Authenticator()(r)

	fmt.Println(errors.Is(wrong, auth.ErrInvalidCredential), right, identity.Level() == auth.LevelMulti)
	// Output: true <nil> true
}

func ExampleGuard_ConfirmHandler() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.StepUp(exampleOTP)
	})
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)

	handler := auth.Middleware(guard.Authenticator())(guard.ConfirmHandler())
	err := exampleChallenge(handler, r, `{"method":"otp","code":"ok"}`)
	identity, _ := guard.Authenticator()(r)

	fmt.Println(err, len(identity.Factors()))
	// Output: <nil> 2
}

func ExampleGuard_MustAccept() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.SecondFactor(exampleOTP)
	})

	guard.MustAccept(auth.MethodOTP)

	fmt.Println("accepted")
	// Output: accepted
}

func ExampleGuard_Fresh() {
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)

	handler := auth.Middleware(guard.Authenticator())(func(_ http.ResponseWriter, r *http.Request) error {
		fmt.Println(guard.Fresh(r))

		return nil
	})

	_ = handler(httptest.NewRecorder(), r)
	// Output: <nil>
}

func ExampleGuard_RecentLogin() {
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)

	fmt.Println(guard.RecentLogin(r, 5*time.Minute))
	// Output: <nil>
}

func ExampleGuardBuilder_StepUp() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.StepUp(exampleOTP)
	})

	guard.MustAccept(auth.MethodOTP)

	fmt.Println(guard != nil)
	// Output: true
}

func ExampleGuardBuilder_FreshFor() {
	guard := exampleGuard(func(policy *auth.GuardBuilder) {
		policy.FreshFor(5 * time.Minute)
	})

	fmt.Println(guard != nil)
	// Output: true
}

func ExampleRequireSession() {
	handler := auth.RequireSession()(router.Handler(func(http.ResponseWriter, *http.Request) error {
		return nil
	}))

	token := auth.NewIdentity("1")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	err := handler(httptest.NewRecorder(), r.WithContext(auth.NewContext(r.Context(), token)))

	fmt.Println(errors.Is(err, auth.ErrUnauthenticated))
	// Output: true
}

func ExampleIdentity_Session() {
	guard := exampleGuard()
	r := exampleRequest()
	_, _ = guard.Begin(r, "1", examplePassword)

	identity, _ := guard.Authenticator()(r)

	fmt.Println(identity.Session(), auth.NewIdentity("1").Session())
	// Output: true false
}

func ExampleExternal() {
	external := auth.External{
		Issuer:        "https://accounts.example.com",
		Subject:       "1089",
		Email:         "ada@example.com",
		EmailVerified: true,
	}

	fmt.Println(external.Issuer, external.EmailVerified)
	// Output: https://accounts.example.com true
}

func ExampleResolver() {
	var resolve auth.Resolver = func(_ context.Context, external auth.External) (string, error) {
		if !external.EmailVerified {
			return "", auth.ErrUnauthenticated
		}

		return "1", nil
	}

	subject, err := resolve(context.Background(), auth.External{Issuer: "https://accounts.example.com", Subject: "1089", EmailVerified: true})

	fmt.Println(subject, err)
	// Output: 1 <nil>
}

func ExampleCredentialEnrolled() {
	events := event.New()

	unsubscribe, err := events.Listen(auth.CredentialEnrolled, func(_ context.Context, enrolled auth.CredentialEnrolledEvent) error {
		fmt.Println(enrolled.Subject, enrolled.Method)

		return nil
	})

	if err != nil {
		panic(err)
	}

	defer unsubscribe()

	events.Emit(context.Background(), auth.CredentialEnrolled, auth.CredentialEnrolledEvent{Subject: "1", Method: auth.MethodOTP})
	// Output: 1 otp
}

func ExampleCredentialEnrolledEvent() {
	enrolled := auth.CredentialEnrolledEvent{Subject: "1", Method: auth.MethodOTP}

	fmt.Println(enrolled.Subject, enrolled.Method)
	// Output: 1 otp
}
