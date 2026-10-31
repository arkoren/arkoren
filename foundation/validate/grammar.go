package validate

import (
	"regexp"
)

// goDurationFormat identifies the toolkit's time.ParseDuration text grammar.
// JSON Schema's standard "duration" means RFC 3339 Appendix A instead; keeping
// the names separate prevents Go text from claiming that incompatible syntax.
const goDurationFormat = "go-duration"

// jsonDurationTimePattern is RFC 3339 Appendix A's dur-time production. Units
// cannot skip an intermediate component: hours may have minutes and minutes
// may have seconds, but hours followed directly by seconds are not this ABNF.
const jsonDurationTimePattern = `T(?:[0-9]+H(?:[0-9]+M(?:[0-9]+S)?)?|[0-9]+M(?:[0-9]+S)?|[0-9]+S)`

// jsonDurationDatePattern is the corresponding dur-date component without its
// optional dur-time suffix. It requires ASCII digits and preserves adjacency
// between years, months and days without imposing numerical component limits.
const jsonDurationDatePattern = `(?:[0-9]+Y(?:[0-9]+M(?:[0-9]+D)?)?|[0-9]+M(?:[0-9]+D)?|[0-9]+D)`

// jsonDurationPattern compiles the complete standard duration syntax once.
// Weeks are a separate alternative, fractions and signs are not in the ABNF,
// and neither time nor date components may be empty. The pattern is immutable.
var jsonDurationPattern = regexp.MustCompile(`^P(?:` + jsonDurationDatePattern +
	`(?:` + jsonDurationTimePattern + `)?|` + jsonDurationTimePattern + `|[0-9]+W)$`)
