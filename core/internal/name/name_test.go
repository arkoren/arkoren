package name_test

import (
	"strings"
	"testing"

	"arkoren.dev/core/internal/name"
)

// checkIdentifier fails the test when name.Identifier accepts or rejects input
// contrary to valid.
func checkIdentifier(t *testing.T, input string, valid bool) {
	t.Helper()

	err := name.Identifier(input)

	if (err == nil) != valid {
		t.Errorf("Identifier(%q) = %v, valid want %t", input, err, valid)
	}
}

// checkRoutable fails the test when name.Routable accepts or rejects input
// contrary to valid.
func checkRoutable(t *testing.T, input string, pattern, valid bool) {
	t.Helper()

	err := name.Routable(input, pattern)

	if (err == nil) != valid {
		t.Errorf("Routable(%q, %t) = %v, valid want %t", input, pattern, err, valid)
	}
}

// TestIdentifierSingleLetter checks the minimum nonempty identifier.
// One lowercase ASCII letter must satisfy the definition-name rules.
func TestIdentifierSingleLetter(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "a", true)
}

// TestIdentifierHyphenated checks an internal hyphen in a definition name.
// Hyphens separate words without introducing routing segments.
func TestIdentifierHyphenated(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "send-mail", true)
}

// TestIdentifierDigits checks numeric characters alongside letters.
// Digits remain valid on both sides of an internal hyphen.
func TestIdentifierDigits(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "a1-b2", true)
}

// TestIdentifierMaxLength checks the inclusive 64-byte identifier boundary.
// A name exactly at the limit must remain usable.
func TestIdentifierMaxLength(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, strings.Repeat("a", 64), true)
}

// TestIdentifierTooLong checks the first byte beyond the identifier limit.
// A 65-byte name must fail even when every character is otherwise valid.
func TestIdentifierTooLong(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, strings.Repeat("a", 65), false)
}

// TestIdentifierEmpty checks that a definition requires an identity.
// An omitted name must not be accepted as a valid identifier.
func TestIdentifierEmpty(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "", false)
}

// TestIdentifierLeadingHyphen checks the left-edge token restriction.
// Hyphens are valid internally but cannot begin an identifier.
func TestIdentifierLeadingHyphen(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "-a", false)
}

// TestIdentifierTrailingHyphen checks the right-edge token restriction.
// Hyphens are valid internally but cannot end an identifier.
func TestIdentifierTrailingHyphen(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "a-", false)
}

// TestIdentifierUppercase checks canonical lowercase identity spelling.
// The validator must reject uppercase rather than normalize it silently.
func TestIdentifierUppercase(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "A", false)
}

// TestIdentifierUnderscore checks the allowed separator vocabulary.
// Underscores must not substitute for the supported internal hyphen.
func TestIdentifierUnderscore(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "a_b", false)
}

// TestIdentifierDot checks separation between identifiers and routing names.
// A definition identifier cannot contain dot-separated segments.
func TestIdentifierDot(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "a.b", false)
}

// TestIdentifierSpace checks that names are machine identifiers, not labels.
// Whitespace within an otherwise valid name must be rejected.
func TestIdentifierSpace(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "a b", false)
}

// TestIdentifierNonASCII checks the ASCII-only definition-name contract.
// A Unicode letter must not pass merely because it is lowercase.
func TestIdentifierNonASCII(t *testing.T) {
	t.Parallel()

	checkIdentifier(t, "é", false)
}

// TestRoutablePlain checks a routing name with one ordinary segment.
// Dots are optional when there is only one token.
func TestRoutablePlain(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user", false, true)
}

// TestRoutableDotted checks a multi-segment event name without wildcards.
// Ordinary dotted names must be accepted when pattern mode is off.
func TestRoutableDotted(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user.created", false, true)
}

// TestRoutableStarWithoutPattern checks literal-name wildcard rejection.
// A publishing name cannot use a subscription-only star segment.
func TestRoutableStarWithoutPattern(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user.*", false, false)
}

// TestRoutableStarPattern checks a complete one-segment wildcard.
// Pattern mode permits a star after an ordinary area prefix.
func TestRoutableStarPattern(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user.*", true, true)
}

// TestRoutableHashPattern checks the terminal multi-segment wildcard.
// A hash may follow an ordinary prefix when it is the final segment.
func TestRoutableHashPattern(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user.#", true, true)
}

// TestRoutableBareHashPattern checks the unrestricted subscription pattern.
// A terminal hash does not require an ordinary prefix.
func TestRoutableBareHashPattern(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "#", true, true)
}

// TestRoutableLeadingStarPattern checks a wildcard before an ordinary suffix.
// Unlike hash, star need not be the final pattern segment.
func TestRoutableLeadingStarPattern(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "*.created", true, true)
}

// TestRoutableLeadingHashPattern checks the terminal-only hash restriction.
// A suffix after hash must be rejected even in pattern mode.
func TestRoutableLeadingHashPattern(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "#.created", true, false)
}

// TestRoutablePartialStarPattern checks wildcard token boundaries.
// A star embedded inside an ordinary segment is not a supported pattern.
func TestRoutablePartialStarPattern(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "us*r.created", true, false)
}

// TestRoutablePartialHashPattern checks that hash occupies its entire token.
// Appending ordinary characters must not create a valid wildcard segment.
func TestRoutablePartialHashPattern(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user.#x", true, false)
}

// TestRoutableEmpty checks that an event has at least one routing segment.
// The absence of a name must not be treated as an unrestricted match.
func TestRoutableEmpty(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "", false, false)
}

// TestRoutableEmptySegment checks adjacent routing separators.
// Every segment between dots must contain a valid token.
func TestRoutableEmptySegment(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user..created", false, false)
}

// TestRoutableLeadingDot checks the first routing segment's presence.
// A leading separator must not be normalized away.
func TestRoutableLeadingDot(t *testing.T) {
	t.Parallel()

	checkRoutable(t, ".user", false, false)
}

// TestRoutableTrailingDot checks the final routing segment's presence.
// A trailing separator must not be normalized away.
func TestRoutableTrailingDot(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user.", false, false)
}

// TestRoutableUppercase checks lowercase rules across all routing segments.
// An uppercase prefix invalidates an otherwise ordinary event name.
func TestRoutableUppercase(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "User.created", false, false)
}

// TestRoutableLeadingHyphenSegment checks token rules after a dot.
// Later segments cannot begin with hyphens any more than the first can.
func TestRoutableLeadingHyphenSegment(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user.-a", false, false)
}

// TestRoutableUnderscore checks separator rules within a later segment.
// A dotted prefix must not make an underscore-bearing token valid.
func TestRoutableUnderscore(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user.a_b", false, false)
}

// TestRoutableNulByte checks embedded control-character rejection.
// Validation must inspect the full name rather than stop at a NUL byte.
func TestRoutableNulByte(t *testing.T) {
	t.Parallel()

	checkRoutable(t, "user\x00.created", false, false)
}

// TestRoutableMaxLength checks the inclusive 255-byte routing-name boundary.
// Many valid short segments may fill the total limit exactly.
func TestRoutableMaxLength(t *testing.T) {
	t.Parallel()

	checkRoutable(t, strings.Repeat("a.", 127)+"a", false, true)
}

// TestRoutableTooLong checks the first byte beyond the total routing limit.
// Individually short segments do not exempt the full name from its cap.
func TestRoutableTooLong(t *testing.T) {
	t.Parallel()

	checkRoutable(t, strings.Repeat("a.", 127)+"ab", false, false)
}

// TestRoutableSegmentTooLong checks the per-token 64-byte restriction.
// A short total name can still fail because one segment exceeds its limit.
func TestRoutableSegmentTooLong(t *testing.T) {
	t.Parallel()

	checkRoutable(t, strings.Repeat("a", 65), false, false)
}
