package restateworkflow

import (
	"strings"
	"unicode"

	"arkoren.dev/driver/restate/restateworkflow/internal/protocol"
)

// serviceNamer maps workflow names to Restate service names.
type serviceNamer struct {
	// base is the prefix in upper camel case.
	base string
}

// newServiceNamer builds the namer for prefix.
func newServiceNamer(prefix string) serviceNamer {
	var builder strings.Builder

	upper := true

	for _, char := range prefix {
		switch {
		case !unicode.IsLetter(char) && !unicode.IsDigit(char):
			upper = true
		case upper:
			builder.WriteRune(unicode.ToUpper(char))

			upper = false
		default:
			builder.WriteRune(char)
		}
	}

	return serviceNamer{
		base: builder.String(),
	}
}

// service returns the Restate service name of the workflow called name. The
// facade allows only [a-z0-9-] in names and Restate allows dashes, so the
// name is kept as is behind the "Wf_" marker, which also guarantees a leading
// letter. The mapping is injective.
func (servicenamer serviceNamer) service(name string) string {
	return servicenamer.base + "Wf_" + name
}

// payload retains the native raw JSON converter at the client boundary.
type payload = protocol.Payload

// runHandler retains the shipped protocol identity.
const runHandler = protocol.RunHandler

// signalHandler retains the shipped protocol identity.
const signalHandler = protocol.SignalHandler

// cancelHandler retains the shipped protocol identity.
const cancelHandler = protocol.CancelHandler

// metadataHeader retains the shipped protocol identity.
const metadataHeader = protocol.MetadataHeader
