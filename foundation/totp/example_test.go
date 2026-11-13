package totp_test

import (
	"errors"
	"fmt"
	"time"

	"arkoren.dev/foundation/totp"
)

// Verify a submitted code and reject a replayed time step.
func ExampleScheme_Verify() {
	scheme := totp.New()
	secret := []byte("12345678901234567890")
	lastStep := int64(0) // Loaded from storage; the highest step accepted so far.

	step, err := scheme.Verify(secret, "287082", time.Unix(59, 0))
	switch {
	case errors.Is(err, totp.ErrInvalidCode):
		fmt.Println("wrong code")
	case err != nil:
		fmt.Println("error:", err)
	case step <= lastStep:
		fmt.Println("replayed")
	default:
		fmt.Println("accepted step", step)
	}
	// Output: accepted step 1
}

// Build a provisioning URI for an authenticator app.
func ExampleScheme_URI() {
	uri, err := totp.New().URI("Example", "alice@example.com", []byte("12345678901234567890"))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(uri)
	// Output: otpauth://totp/Example:alice@example.com?algorithm=SHA1&digits=6&issuer=Example&period=30&secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ
}

// Configure an eight-digit SHA-256 scheme.
func ExampleNew() {
	scheme := totp.New(func(scheme *totp.Builder) {
		scheme.Algorithm(totp.SHA256).Digits(8)
	})
	code, err := scheme.Generate([]byte("12345678901234567890123456789012"), time.Unix(59, 0))
	fmt.Println(code, err)
	// Output: 46119246 <nil>
}
