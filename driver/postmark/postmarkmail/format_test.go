package postmarkmail_test

import (
	"fmt"
	"strings"
	"testing"

	"arkoren.dev/driver/postmark/postmarkmail"
)

// ExampleDriver_Format demonstrates the public formatting protocol's credential
// boundary without constructing a request or revealing the synthetic token.
func ExampleDriver_Format() {
	driver, err := postmarkmail.New("server-token")

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Printf("%#v\n", driver)
	// Output: postmarkmail.Driver
}

// TestDriverFormattingDoesNotExposeServerToken exercises the public fmt
// protocol on pointers and copied values. Unexported credential fields must not
// become visible through reflection when callers log their configured driver.
func TestDriverFormattingDoesNotExposeServerToken(t *testing.T) {
	t.Parallel()

	driver, err := postmarkmail.New("synthetic-server-token-marker")

	if err != nil {
		t.Fatal(err)
	}

	output := fmt.Sprintf("%v %+v %#v | %v %+v %#v", driver, driver, driver, *driver, *driver, *driver)

	if strings.Contains(output, "synthetic-server-token-marker") {
		t.Fatal("driver formatting exposed the Postmark server token")
	}
}
