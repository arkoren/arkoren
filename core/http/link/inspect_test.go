package link_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"arkoren.dev/core/http/link"
)

// ExampleSigner_Inspect shows reading verified link metadata without exposing
// the signature in the resulting application URL.
func ExampleSigner_Inspect() {
	signer, err := link.New(secret)

	if err != nil {
		fmt.Println(err)

		return
	}

	signed, err := signer.Temporary("/reset?user=42", time.Hour)

	if err != nil {
		fmt.Println(err)

		return
	}

	details, err := signer.Inspect(context.Background(), signed)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(err, details.URL, details.Temporary, details.Expired)
	// Output: <nil> /reset?user=42 true false
}

// TestInspectMalformedURLReturnsNoDetails verifies that parse errors remain URL
// errors, not signature errors, and do not publish unauthenticated metadata.
func TestInspectMalformedURLReturnsNoDetails(t *testing.T) {
	t.Parallel()

	signer := mustNewSigner(t)

	details, err := signer.Inspect(t.Context(), "%")

	if !errors.Is(err, link.ErrInvalidURL) || details != (link.Inspection{}) {
		t.Fatalf("Inspect = %+v, %v; want empty details and ErrInvalidURL", details, err)
	}
}
