package validate_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"testing"

	"arkoren.dev/foundation/validate"
)

// TestStringDurationRule checks the string duration constraint against valid
// text and malformed values.
//
// Example:
//
//	go test -run "^TestStringDurationRule$" .
func TestStringDurationRule(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Duration()
	})

	for _, valid := range []string{"182ms", "1h30m", "-3s", "1.5h", ""} {
		if err := schema.Validate(context.Background(), valid); err != nil {
			t.Fatalf("%q: %v", valid, err)
		}
	}

	for _, invalid := range []string{"soon", "12", "5 minutes"} {
		requireCode(t, schema.Validate(context.Background(), invalid), "string.duration")
	}

	wantSchema(t, schema, `{"format":"go-duration","type":"string"}`)
}

// TestStringDurationMessage checks the built-in English message for a duration
// violation.
//
// Example:
//
//	go test -run "^TestStringDurationMessage$" .
func TestStringDurationMessage(t *testing.T) {
	t.Parallel()

	errs, ok := errors.AsType[validate.Errors](validate.String(func(value *validate.StringBuilder[string]) {
		value.Duration()
	}).Validate(context.Background(), "soon"))

	if !ok {
		t.Fatal("not validate.Errors")
	}

	if got := errs[""][0].Message; got != "must be a valid duration" {
		t.Fatalf("message = %q", got)
	}
}

// ExampleString_named validates a named string type.
func ExampleString_named() {
	type color string

	schema := validate.String(func(value *validate.StringBuilder[color]) {
		value.OneOf("red", "green")
	})

	fmt.Println(schema.Validate(context.Background(), "red"))
	fmt.Println(schema.Validate(context.Background(), "blue"))

	// Output:
	// <nil>
	// validation failed: (root): must be one of: red, green
}

// TestNormalizersAreImmutableParent verifies deriving siblings with further
// normalizers leaves the parent schema trimming only.
func TestNormalizersAreImmutableParent(t *testing.T) {
	t.Parallel()

	parent := validate.String(func(value *validate.StringBuilder[string]) {
		value.Trim()
	})

	_ = validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(parent)
		value.ToUpper()
	})
	_ = validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(parent)
		value.ToLower()
	})

	requireParse(t, parent, " Ab ", "Ab")
}

// TestNormalizersAreImmutableUpper verifies a sibling derived with ToUpper
// trims and upper-cases.
func TestNormalizersAreImmutableUpper(t *testing.T) {
	t.Parallel()

	parent := validate.String(func(value *validate.StringBuilder[string]) {
		value.Trim()
	})
	upper := validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(parent)
		value.ToUpper()
	})

	_ = validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(parent)
		value.ToLower()
	})

	requireParse(t, upper, " Ab ", "AB")
}

// TestNormalizersAreImmutableLower verifies a sibling derived with ToLower
// trims and lower-cases.
func TestNormalizersAreImmutableLower(t *testing.T) {
	t.Parallel()

	parent := validate.String(func(value *validate.StringBuilder[string]) {
		value.Trim()
	})

	_ = validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(parent)
		value.ToUpper()
	})

	lower := validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(parent)
		value.ToLower()
	})

	requireParse(t, lower, " Ab ", "ab")
}

// TestNormalizeStringsLower checks Unicode-aware lowercase conversion in
// parsed output.
//
// Example:
//
//	go test -run "^TestNormalizeStringsLower$" .
func TestNormalizeStringsLower(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.ToLower()
	}), "AbÇ", "abç")
}

// TestNormalizeStringsUpper checks Unicode-aware uppercase conversion in
// parsed output.
//
// Example:
//
//	go test -run "^TestNormalizeStringsUpper$" .
func TestNormalizeStringsUpper(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.ToUpper()
	}), "abç", "ABÇ")
}

// TestNormalizeStringsPrefix checks that prefix trimming removes only the
// matching leading occurrence.
//
// Example:
//
//	go test -run "^TestNormalizeStringsPrefix$" .
func TestNormalizeStringsPrefix(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.TrimPrefix("ab")
	}), "ababc", "abc")
}

// TestNormalizeStringsSuffix checks that suffix trimming removes only the
// matching trailing occurrence.
//
// Example:
//
//	go test -run "^TestNormalizeStringsSuffix$" .
func TestNormalizeStringsSuffix(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.TrimSuffix("c")
	}), "acc", "ac")
}

// TestNormalizeStringsTruncate checks truncation by Unicode code points rather
// than encoded bytes.
//
// Example:
//
//	go test -run "^TestNormalizeStringsTruncate$" .
func TestNormalizeStringsTruncate(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Truncate(2)
	}), "héllo", "hé")
}

// TestNormalizeStringsTruncateShort checks that a string shorter than the
// truncation limit remains unchanged.
//
// Example:
//
//	go test -run "^TestNormalizeStringsTruncateShort$" .
func TestNormalizeStringsTruncateShort(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Truncate(9)
	}), "hé", "hé")
}

// TestNormalizeStringsTruncateZero checks that a zero truncation limit
// produces an empty string.
//
// Example:
//
//	go test -run "^TestNormalizeStringsTruncateZero$" .
func TestNormalizeStringsTruncateZero(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Truncate(0)
	}), "hé", "")
}

// TestNormalizeStringsOrder checks that prefix removal feeds its result into
// the following whitespace trim.
//
// Example:
//
//	go test -run "^TestNormalizeStringsOrder$" .
func TestNormalizeStringsOrder(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.TrimPrefix("x")
		value.Trim()
	}), "x  a ", "a")
}

// TestNormalizeStringsOrderReversed checks that reversing normalizer
// registration changes the result in the expected way.
//
// Example:
//
//	go test -run "^TestNormalizeStringsOrderReversed$" .
func TestNormalizeStringsOrderReversed(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Trim()
		value.TrimPrefix("x")
	}), "x  a ", "  a")
}

// TestNormalizeStringsCustom checks that a custom normalizer receives the
// preceding built-in normalizer result.
//
// Example:
//
//	go test -run "^TestNormalizeStringsCustom$" .
func TestNormalizeStringsCustom(t *testing.T) {
	t.Parallel()

	requireParse(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.ToUpper()
		value.Normalize(func(s string) string {
			return s + "!"
		})
	}), "a", "A!")
}

// TestNormalizeStringsInvalidArguments checks construction failures for
// negative truncation limits and nil normalizers.
//
// Example:
//
//	go test -run "^TestNormalizeStringsInvalidArguments$" .
func TestNormalizeStringsInvalidArguments(t *testing.T) {
	t.Parallel()

	mustPanicInvalid(t, func() {
		validate.String(func(value *validate.StringBuilder[string]) {
			value.Truncate(-1)
		})
	})
	mustPanicInvalid(t, func() {
		validate.String(func(value *validate.StringBuilder[string]) {
			value.Normalize(nil)
		})
	})
}

// TestNormalizeNamedTypes checks that string, integer and float normalizers
// retain named result types.
//
// Example:
//
//	go test -run "^TestNormalizeNamedTypes$" .
func TestNormalizeNamedTypes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	name, err := validate.String(func(value *validate.StringBuilder[normName]) {
		value.ToUpper()
		value.TrimPrefix("A")
		value.Truncate(2)
	}).Parse(ctx, "abcd")

	if err != nil || name != "BC" {
		t.Fatalf("string = %q, %v", name, err)
	}

	meters, err := validate.Int(func(number *validate.IntBuilder[normMeters]) {
		number.Abs()
		number.Clamp(0, 10)
	}).Parse(ctx, -50)

	if err != nil || meters != 10 {
		t.Fatalf("int = %v, %v", meters, err)
	}

	ratio, err := validate.Float(func(number *validate.FloatBuilder[normRatio]) {
		number.Round(1)
	}).Parse(ctx, 0.25)

	if err != nil || ratio != 0.3 {
		t.Fatalf("float = %v, %v", ratio, err)
	}
}

// TestNormalizeEnum checks that normalization runs before enum membership
// validation.
//
// Example:
//
//	go test -run "^TestNormalizeEnum$" .
func TestNormalizeEnum(t *testing.T) {
	t.Parallel()

	got, err := validate.String(func(value *validate.StringBuilder[normName]) {
		value.OneOf("a", "b")
		value.ToLower()
	}).Parse(context.Background(), "A")

	if err != nil || got != "a" {
		t.Fatalf("enum = %q, %v", got, err)
	}
}

// TestStringRulesMinPass verifies the MinPass string rule case.
func TestStringRulesMinPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}).Validate(context.Background(), "abc"), "")
}

// TestStringRulesMinFail verifies the MinFail string rule case.
func TestStringRulesMinFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}).Validate(context.Background(), "ab"), "string.min")
}

// TestStringRulesMinCountsRunes verifies the MinCountsRunes string rule case.
func TestStringRulesMinCountsRunes(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}).Validate(context.Background(), "日本語"), "")
}

// TestStringRulesMinAbsent verifies the MinAbsent string rule case.
func TestStringRulesMinAbsent(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}).Validate(context.Background(), ""), "")
}

// TestStringRulesMaxPass verifies the MaxPass string rule case.
func TestStringRulesMaxPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Max(3)
	}).Validate(context.Background(), "abc"), "")
}

// TestStringRulesMaxFail verifies the MaxFail string rule case.
func TestStringRulesMaxFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Max(3)
	}).Validate(context.Background(), "abcd"), "string.max")
}

// TestStringRulesMaxCountsRunes verifies the MaxCountsRunes string rule case.
func TestStringRulesMaxCountsRunes(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Max(3)
	}).Validate(context.Background(), "日本語日"), "string.max")
}

// TestStringRulesLenPass verifies the LenPass string rule case.
func TestStringRulesLenPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Len(2)
	}).Validate(context.Background(), "éé"), "")
}

// TestStringRulesLenFail verifies the LenFail string rule case.
func TestStringRulesLenFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Len(2)
	}).Validate(context.Background(), "abc"), "string.len")
}

// TestStringRulesNotBlankPass verifies the NotBlankPass string rule case.
func TestStringRulesNotBlankPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.NotBlank()
	}).Validate(context.Background(), " a "), "")
}

// TestStringRulesNotBlankFail verifies the NotBlankFail string rule case.
func TestStringRulesNotBlankFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.NotBlank()
	}).Validate(context.Background(), "  \t"), "string.not_blank")
}

// TestStringRulesRegexPass verifies the RegexPass string rule case.
func TestStringRulesRegexPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Regex(regexp.MustCompile(`^\d+$`))
	}).Validate(context.Background(), "123"), "")
}

// TestStringRulesRegexFail verifies the RegexFail string rule case.
func TestStringRulesRegexFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Regex(regexp.MustCompile(`^\d+$`))
	}).Validate(context.Background(), "12a"), "string.regex")
}

// TestStringRulesPrefixPass verifies the PrefixPass string rule case.
func TestStringRulesPrefixPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Prefix("ab")
	}).Validate(context.Background(), "abc"), "")
}

// TestStringRulesPrefixFail verifies the PrefixFail string rule case.
func TestStringRulesPrefixFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Prefix("ab")
	}).Validate(context.Background(), "bc"), "string.prefix")
}

// TestStringRulesSuffixPass verifies the SuffixPass string rule case.
func TestStringRulesSuffixPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Suffix("bc")
	}).Validate(context.Background(), "abc"), "")
}

// TestStringRulesSuffixFail verifies the SuffixFail string rule case.
func TestStringRulesSuffixFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Suffix("bc")
	}).Validate(context.Background(), "ab"), "string.suffix")
}

// TestStringRulesContainsPass verifies the ContainsPass string rule case.
func TestStringRulesContainsPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Contains("b")
	}).Validate(context.Background(), "abc"), "")
}

// TestStringRulesContainsFail verifies the ContainsFail string rule case.
func TestStringRulesContainsFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Contains("z")
	}).Validate(context.Background(), "abc"), "string.contains")
}

// TestStringRulesEmailPass verifies the EmailPass string rule case.
func TestStringRulesEmailPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Email()
	}).Validate(context.Background(), "foo@bar.baz"), "")
}

// TestStringRulesEmailPlusPass verifies the EmailPlusPass string rule case.
func TestStringRulesEmailPlusPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Email()
	}).Validate(context.Background(), "foo+tag@bar.com"), "")
}

// TestStringRulesEmailFail verifies the EmailFail string rule case.
func TestStringRulesEmailFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Email()
	}).Validate(context.Background(), "foo.bar"), "string.email")
}

// TestStringRulesEmailDisplayNameFail verifies the EmailDisplayNameFail string rule case.
func TestStringRulesEmailDisplayNameFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Email()
	}).Validate(context.Background(), "Foo <foo@bar.com>"), "string.email")
}

// TestStringRulesURLPass verifies the URLPass string rule case.
func TestStringRulesURLPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.URL()
	}).Validate(context.Background(), "https://erik.cat/path?q=1"), "")
}

// TestStringRulesURLFailNoScheme verifies the URLFailNoScheme string rule case.
func TestStringRulesURLFailNoScheme(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.URL()
	}).Validate(context.Background(), "erik.cat"), "string.url")
}

// TestStringRulesURLFailNoHost verifies the URLFailNoHost string rule case.
func TestStringRulesURLFailNoHost(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.URL()
	}).Validate(context.Background(), "https://"), "string.url")
}

// TestStringRulesURLFailJavascript verifies that scripting schemes are rejected.
func TestStringRulesURLFailJavascript(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"javascript://x.com/%0aalert(1)", "data://x.com/text", "data:text/html,hi", "ftp://erik.cat/file"} {
		requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
			value.URL()
		}).Validate(context.Background(), value), "string.url")
	}
}

// TestStringRulesURLSchemesCaseInsensitive verifies that URL accepts the listed schemes in any case.
func TestStringRulesURLSchemesCaseInsensitive(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.URL()
	}).Validate(context.Background(), "HTTPS://erik.cat"), "")
	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.URL("FTP")
	}).Validate(context.Background(), "ftp://erik.cat/file"), "")
}

// TestStringRulesURLCustomSchemesReplaceDefault verifies that listed schemes replace http and https.
func TestStringRulesURLCustomSchemesReplaceDefault(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.URL("ftp")
	})

	requireCode(t, schema.Validate(context.Background(), "ftp://erik.cat/file"), "")
	requireCode(t, schema.Validate(context.Background(), "https://erik.cat"), "string.url")
}

// TestStringRulesUUIDPass verifies the UUIDPass string rule case.
func TestStringRulesUUIDPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.UUID()
	}).Validate(context.Background(), "123e4567-e89b-12d3-a456-426614174000"), "")
}

// TestStringRulesUUIDUpperPass verifies the UUIDUpperPass string rule case.
func TestStringRulesUUIDUpperPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.UUID()
	}).Validate(context.Background(), "123E4567-E89B-12D3-A456-426614174000"), "")
}

// TestStringRulesUUIDFailShort verifies the UUIDFailShort string rule case.
func TestStringRulesUUIDFailShort(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.UUID()
	}).Validate(context.Background(), "123e4567"), "string.uuid")
}

// TestStringRulesUUIDFailChars verifies the UUIDFailChars string rule case.
func TestStringRulesUUIDFailChars(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.UUID()
	}).Validate(context.Background(), "123e4567-e89b-12d3-a456-42661417400z"), "string.uuid")
}

// TestStringRulesUUIDFailDashes verifies the UUIDFailDashes string rule case.
func TestStringRulesUUIDFailDashes(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.UUID()
	}).Validate(context.Background(), "123e4567xe89b-12d3-a456-426614174000"), "string.uuid")
}

// TestStringRulesDomainPass verifies the DomainPass string rule case.
func TestStringRulesDomainPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Domain()
	}).Validate(context.Background(), "erik.cat"), "")
}

// TestStringRulesDomainSubPass verifies the DomainSubPass string rule case.
func TestStringRulesDomainSubPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Domain()
	}).Validate(context.Background(), "a-b.example.co"), "")
}

// TestStringRulesDomainFailOneLabel verifies the DomainFailOneLabel string rule case.
func TestStringRulesDomainFailOneLabel(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Domain()
	}).Validate(context.Background(), "bad-domain"), "string.domain")
}

// TestStringRulesDomainFailTLDDigits verifies the DomainFailTLDDigits string rule case.
func TestStringRulesDomainFailTLDDigits(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Domain()
	}).Validate(context.Background(), "erik.c4"), "string.domain")
}

// TestStringRulesDomainFailHyphenEdge verifies the DomainFailHyphenEdge string rule case.
func TestStringRulesDomainFailHyphenEdge(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Domain()
	}).Validate(context.Background(), "-erik.cat"), "string.domain")
}

// TestStringRulesDomainFailEmptyLabel verifies the DomainFailEmptyLabel string rule case.
func TestStringRulesDomainFailEmptyLabel(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Domain()
	}).Validate(context.Background(), "erik..cat"), "string.domain")
}

// TestStringRulesIPv4Pass verifies the IPv4Pass string rule case.
func TestStringRulesIPv4Pass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.IP()
	}).Validate(context.Background(), "192.168.0.1"), "")
}

// TestStringRulesIPv6Pass verifies the IPv6Pass string rule case.
func TestStringRulesIPv6Pass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.IP()
	}).Validate(context.Background(), "2001:db8::1"), "")
}

// TestStringRulesIPFail verifies the IPFail string rule case.
func TestStringRulesIPFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.IP()
	}).Validate(context.Background(), "300.1.1.1"), "string.ip")
}

// TestStringRulesIPZoneFail verifies the IPZoneFail string rule case.
func TestStringRulesIPZoneFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.IP()
	}).Validate(context.Background(), "fe80::1%eth0"), "string.ip")
}

// TestStringRulesLowercasePass verifies the LowercasePass string rule case.
func TestStringRulesLowercasePass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Lowercase()
	}).Validate(context.Background(), "abc-1"), "")
}

// TestStringRulesLowercaseFail verifies the LowercaseFail string rule case.
func TestStringRulesLowercaseFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Lowercase()
	}).Validate(context.Background(), "aBc"), "string.lowercase")
}

// TestStringRulesUppercasePass verifies the UppercasePass string rule case.
func TestStringRulesUppercasePass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Uppercase()
	}).Validate(context.Background(), "ABC"), "")
}

// TestStringRulesUppercaseFail verifies the UppercaseFail string rule case.
func TestStringRulesUppercaseFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Uppercase()
	}).Validate(context.Background(), "ABc"), "string.uppercase")
}

// TestStringRulesAlphaPass verifies the AlphaPass string rule case.
func TestStringRulesAlphaPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Alpha()
	}).Validate(context.Background(), "abcXYZ"), "")
}

// TestStringRulesAlphaFail verifies the AlphaFail string rule case.
func TestStringRulesAlphaFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Alpha()
	}).Validate(context.Background(), "abc1"), "string.alpha")
}

// TestStringRulesAlphaNumericPass verifies the AlphaNumericPass string rule case.
func TestStringRulesAlphaNumericPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.AlphaNumeric()
	}).Validate(context.Background(), "abc123"), "")
}

// TestStringRulesAlphaNumericFail verifies the AlphaNumericFail string rule case.
func TestStringRulesAlphaNumericFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.AlphaNumeric()
	}).Validate(context.Background(), "abc-123"), "string.alphanumeric")
}

// TestStringRulesAlphaDashPass verifies the AlphaDashPass string rule case.
func TestStringRulesAlphaDashPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.AlphaDash()
	}).Validate(context.Background(), "ab_c-1"), "")
}

// TestStringRulesAlphaDashFail verifies the AlphaDashFail string rule case.
func TestStringRulesAlphaDashFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.AlphaDash()
	}).Validate(context.Background(), "ab c"), "string.alpha_dash")
}

// TestStringRulesASCIIPass verifies the ASCIIPass string rule case.
func TestStringRulesASCIIPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.ASCII()
	}).Validate(context.Background(), "hello world!"), "")
}

// TestStringRulesASCIIFail verifies the ASCIIFail string rule case.
func TestStringRulesASCIIFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.ASCII()
	}).Validate(context.Background(), "héllo"), "string.ascii")
}

// TestStringRulesOneOfPass verifies the OneOfPass string rule case.
func TestStringRulesOneOfPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.OneOf("a", "b")
	}).Validate(context.Background(), "a"), "")
}

// TestStringRulesOneOfFail verifies the OneOfFail string rule case.
func TestStringRulesOneOfFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.OneOf("a", "b")
	}).Validate(context.Background(), "c"), "one_of")
}

// TestStringRulesNotOneOfPass verifies the NotOneOfPass string rule case.
func TestStringRulesNotOneOfPass(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.NotOneOf("a")
	}).Validate(context.Background(), "b"), "")
}

// TestStringRulesNotOneOfFail verifies the NotOneOfFail string rule case.
func TestStringRulesNotOneOfFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.NotOneOf("a")
	}).Validate(context.Background(), "a"), "not_one_of")
}

// TestStringRulesEnumFail verifies the EnumFail string rule case.
func TestStringRulesEnumFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.OneOf("x", "y")
	}).Validate(context.Background(), "z"), "one_of")
}

// TestStringRulesRequiredFail verifies the RequiredFail string rule case.
func TestStringRulesRequiredFail(t *testing.T) {
	t.Parallel()

	requireCode(t, validate.String(func(value *validate.StringBuilder[string]) {
		value.Required()
	}).Validate(context.Background(), ""), "required")
}

// role is a named string enum used to check typed string schemas. Construction
// and parsing must retain the named type.
type role string

// TestStringNamedType verifies String and OneOf accept named string types.
func TestStringNamedType(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(value *validate.StringBuilder[role]) {
		value.OneOf("admin", "editor")
	})

	if err := schema.Validate(context.Background(), "admin"); err != nil {
		t.Fatal(err)
	}

	requireCodes(t, schema.Validate(context.Background(), "guest"), map[string][]string{"": {"one_of"}})

	typed := validate.String(func(value *validate.StringBuilder[role]) {
		value.Min(2)
	})

	requireCodes(t, typed.Validate(context.Background(), "a"), map[string][]string{"": {"string.min"}})
}

// TestStringParams verifies violation params and the rendered message.
func TestStringParams(t *testing.T) {
	t.Parallel()

	err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(3)
	}).Validate(context.Background(), "ab")

	errs, _ := errors.AsType[validate.Errors](err)
	violation := errs[""][0]

	if violation.Code != "string.min" || violation.Params["min"] != 3 {
		t.Fatalf("unexpected violation %#v", violation)
	}

	if violation.Message != "must be at least 3 characters" {
		t.Fatalf("message = %q", violation.Message)
	}
}

// TestStringBuildersAreImmutable verifies a builder call never changes the
// schema it was called on.
func TestStringBuildersAreImmutable(t *testing.T) {
	t.Parallel()

	base := validate.String(func(value *validate.StringBuilder[string]) {
		value.Min(2)
	})
	stricter := validate.String(func(value *validate.StringBuilder[string]) {
		value.Use(base)
		value.Max(3)
	})

	if err := base.Validate(context.Background(), "abcdef"); err != nil {
		t.Fatalf("base schema changed: %v", err)
	}

	requireCodes(t, stricter.Validate(context.Background(), "abcdef"), map[string][]string{"": {"string.max"}})
}

// TestStringInvalidBuilders verifies programmer errors panic.
func TestStringInvalidBuilders(t *testing.T) {
	t.Parallel()

	requirePanic(t, func() {
		validate.String(func(value *validate.StringBuilder[string]) {
			value.Regex(nil)
		})
	})
	requirePanic(t, func() {
		validate.String(func(value *validate.StringBuilder[string]) {
			value.Check(nil)
		})
	})
	requirePanic(t, func() {
		validate.String(func(value *validate.StringBuilder[string]) {
			value.Required()
			value.Default("a")
		})
	})
	requirePanic(t, func() {
		validate.String(func(value *validate.StringBuilder[string]) {
			value.Default("a")
			value.Required()
		})
	})
}

// trimRole is a named string enum used by trimming tests. Normalization must
// happen before membership validation without changing the type.
type trimRole string

// trimForm combines nested strings with a sibling observation target. Tests
// verify trimming order and caller-storage isolation across all child roles.
type trimForm struct {
	// Name holds the name observed by string rules or sibling conditions.
	Name string
	// Other provides an independent sibling for comparison or registration
	// checks.
	Other string
	// Role selects the identity or condition branch under test.
	Role trimRole
	// Ptr distinguishes an absent pointer from a present zero pointee.
	Ptr *string
	// Tags provides slice storage for child rules and copy-isolation checks.
	Tags []string
	// Labels provides map storage for child rules and copy-isolation checks.
	Labels map[string]string
}

// TestTrimEmail checks that trimming permits padded addresses while the
// unnormalized email rule still rejects them.
//
// Example:
//
//	go test -run "^TestTrimEmail$" .
func TestTrimEmail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.String(func(value *validate.StringBuilder[string]) {
		value.Trim()
		value.Email()
	})

	got, err := schema.Parse(ctx, " foo@bar.baz\n")

	if err != nil || got != "foo@bar.baz" {
		t.Fatalf("Parse = %q, %v", got, err)
	}

	if err := schema.Validate(ctx, " foo@bar.baz "); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	_, err = validate.String(func(value *validate.StringBuilder[string]) {
		value.Email()
	}).Parse(ctx, " foo@bar.baz")

	requireCodes(t, err, map[string][]string{"": {"string.email"}})
}

// TestTrimPresence checks that whitespace-only input becomes absent before
// Required, defaults and optional value rules.
//
// Example:
//
//	go test -run "^TestTrimPresence$" .
func TestTrimPresence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Trim()
		value.Required()
		value.Min(3)
	}).Validate(ctx, "   ")

	requireCodes(t, err, map[string][]string{"": {"required"}})

	got, err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Default("x")
		value.Trim()
	}).Parse(ctx, " \t ")

	if err != nil || got != "x" {
		t.Fatalf("Parse = %q, %v", got, err)
	}

	if err := validate.String(func(value *validate.StringBuilder[string]) {
		value.Trim()
		value.Min(3)
	}).Validate(ctx, "  "); err != nil {
		t.Fatalf("absent value should skip rules: %v", err)
	}
}

// TestTrimPositionIndependent checks that Trim runs before value rules
// regardless of its registration position.
//
// Example:
//
//	go test -run "^TestTrimPositionIndependent$" .
func TestTrimPositionIndependent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	left := validate.String(func(value *validate.StringBuilder[string]) {
		value.Email()
		value.Trim()
	})
	right := validate.String(func(value *validate.StringBuilder[string]) {
		value.Trim()
		value.Email()
	})

	for _, in := range []string{" a@b.co ", "bad", "  "} {
		l, lerr := left.Parse(ctx, in)
		r, rerr := right.Parse(ctx, in)

		if l != r || (lerr == nil) != (rerr == nil) {
			t.Fatalf("%q: (%q, %v) != (%q, %v)", in, l, lerr, r, rerr)
		}
	}
}

// TestTrimNamedAndEnum checks trimming and enum validation while retaining a
// named string type.
//
// Example:
//
//	go test -run "^TestTrimNamedAndEnum$" .
func TestTrimNamedAndEnum(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	got, err := validate.String(func(value *validate.StringBuilder[trimRole]) {
		value.OneOf("admin", "user")
		value.Trim()
	}).Parse(ctx, " admin ")

	if err != nil || got != "admin" {
		t.Fatalf("Parse = %q, %v", got, err)
	}

	got, err = validate.String(func(value *validate.StringBuilder[trimRole]) {
		value.Trim()
	}).Parse(ctx, " x ")

	if err != nil || got != "x" {
		t.Fatalf("Parse = %q, %v", got, err)
	}
}

// TestTrimNested checks trimming in nested pointers and collections without
// mutating caller-owned values.
//
// Example:
//
//	go test -run "^TestTrimNested$" .
func TestTrimNested(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.Struct[trimForm](func(object *validate.StructBuilder[trimForm]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Trim()
			value.Required()
		})
		object.Field("Ptr", func(pointer *validate.PtrBuilder[string]) {
			pointer.Value(func(value *validate.StringBuilder[string]) {
				value.Trim()
				value.Email()
			})
		})
		object.Field("Tags", func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
				value.Trim()
				value.Min(1)
			})
		})
		object.Field("Labels", func(entries *validate.MapBuilder[string, string]) {
			entries.Keys(func(value *validate.StringBuilder[string]) {
			})
			entries.Values(func(value *validate.StringBuilder[string]) {
				value.Trim()
				value.Min(1)
			})
		})
	})

	pointer := " a@b.co "
	input := trimForm{
		Name:   "  ann ",
		Ptr:    &pointer,
		Tags:   []string{" a ", "b "},
		Labels: map[string]string{"k": " v "},
	}

	got, err := schema.Parse(ctx, input)

	if err != nil {
		t.Fatal(err)
	}

	if got.Name != "ann" || *got.Ptr != "a@b.co" || got.Tags[0] != "a" || got.Tags[1] != "b" || got.Labels["k"] != "v" {
		t.Fatalf("not trimmed: %+v", got)
	}

	if input.Name != "  ann " || *input.Ptr != " a@b.co " || input.Tags[0] != " a " ||
		input.Tags[1] != "b " || input.Labels["k"] != " v " {
		t.Fatalf("input mutated: %+v", input)
	}

	if err := schema.Validate(ctx, input); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if input.Name != "  ann " || input.Tags[0] != " a " {
		t.Fatalf("input mutated by Validate: %+v", input)
	}

	_, err = schema.Parse(ctx, trimForm{
		Name: "  ",
	})

	requireCodes(t, err, map[string][]string{"/Name": {"required"}})
}

// TestTrimCondition checks that sibling presence and equality rules observe
// trimmed values.
//
// Example:
//
//	go test -run "^TestTrimCondition$" .
func TestTrimCondition(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	schema := validate.Struct[trimForm](func(object *validate.StructBuilder[trimForm]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Trim()
		})
		object.Field("Other", func(value *validate.StringBuilder[string]) {
			value.RequiredIf(func(condition *validate.ConditionBuilder) {
				condition.Present("Name")
			})
		})
	})

	if err := schema.Validate(ctx, trimForm{
		Name: "   ",
	}); err != nil {
		t.Fatalf("blank sibling should be absent: %v", err)
	}

	err := schema.Validate(ctx, trimForm{
		Name: " x ",
	})

	requireCodes(t, err, map[string][]string{"/Other": {"required_if"}})

	eq := validate.Struct[trimForm](func(object *validate.StructBuilder[trimForm]) {
		object.Field("Name", func(value *validate.StringBuilder[string]) {
			value.Trim()
		})
		object.Field("Other", func(value *validate.StringBuilder[string]) {
			value.EqField("Name")
		})
	})

	if err := eq.Validate(ctx, trimForm{
		Name:  " x ",
		Other: "x",
	}); err != nil {
		t.Fatalf("EqField should see trimmed value: %v", err)
	}
}

// TestURLPolicyCapturesSchemesBeforeLaterCallbacks checks that URL owns its
// scheme list when the rule is registered, not only when construction ends.
// Construction callbacks must run once, synchronously and in order; schema
// execution and inspection must not replay them.
//
// Example:
//
//	go test -run '^TestURLPolicyCapturesSchemesBeforeLaterCallbacks$' .
func TestURLPolicyCapturesSchemesBeforeLaterCallbacks(t *testing.T) {
	t.Parallel()

	schemes := []string{"HTTPS"}
	var calls []string

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		calls = append(calls, "url")
		text.URL(schemes...)
	}, func(_ *validate.StringBuilder[string]) {
		calls = append(calls, "mutation")
		schemes[0] = "ftp"
	})

	if !slices.Equal(calls, []string{"url", "mutation"}) {
		t.Fatalf("construction callbacks = %v, want [url mutation]", calls)
	}

	requireCode(t, schema.Validate(context.Background(), "https://example.com/path"), "")
	requireCode(t, schema.Validate(context.Background(), "ftp://example.com/path"), "string.url")
	requireParse(t, schema, "https://example.com/path", "https://example.com/path")
	wantSchema(t, schema, `{"format":"uri","type":"string"}`)

	if !slices.Equal(calls, []string{"url", "mutation"}) {
		t.Fatalf("callbacks after execution and inspection = %v, want [url mutation]", calls)
	}
}

// TestURLPolicyDetachesCallerSchemesAfterPublication checks that mutating a
// supplied slice cannot change a published schema's acceptance policy. Parse
// and Validate must retain the same case-insensitive, replacement-only list.
//
// Example:
//
//	go test -run '^TestURLPolicyDetachesCallerSchemesAfterPublication$' .
func TestURLPolicyDetachesCallerSchemesAfterPublication(t *testing.T) {
	t.Parallel()

	schemes := []string{"FTP"}

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.URL(schemes...)
	})

	schemes[0] = "https"

	requireParse(t, schema, "ftp://example.com/file", "ftp://example.com/file")
	requireCode(t, schema.Validate(context.Background(), "ftp://example.com/file"), "")
	requireCode(t, schema.Validate(context.Background(), "https://example.com"), "string.url")
	wantSchema(t, schema, `{"format":"uri","type":"string"}`)
}

// TestURLPolicyEmptySchemeRejectsRelativeReferences checks that an explicit
// empty scheme cannot admit a host-bearing relative reference. This remains
// a runtime string.url failure, not a schema construction error, and Parse
// must return its documented zero result on failure.
//
// Example:
//
//	go test -run '^TestURLPolicyEmptySchemeRejectsRelativeReferences$' .
func TestURLPolicyEmptySchemeRejectsRelativeReferences(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.URL("")
	})

	requireCode(t, schema.Validate(context.Background(), "//example.com/path"), "string.url")

	parsed, err := schema.Parse(context.Background(), "//example.com/path")

	requireCode(t, err, "string.url")

	if parsed != "" {
		t.Fatalf("Parse returned %q, want zero string on failure", parsed)
	}

	requireCode(t, schema.Validate(context.Background(), "https://example.com"), "string.url")
	wantSchema(t, schema, `{"format":"uri","type":"string"}`)
}

// TestURLPolicyEmptySchemeKeepsExplicitAbsoluteSchemes checks that an empty
// entry neither disables a valid listed scheme nor bypasses the independent
// absolute-URL requirement. Custom schemes still replace the default set.
//
// Example:
//
//	go test -run '^TestURLPolicyEmptySchemeKeepsExplicitAbsoluteSchemes$' .
func TestURLPolicyEmptySchemeKeepsExplicitAbsoluteSchemes(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.URL("", "hTtPs")
	})

	requireParse(t, schema, "HTTPS://example.com/path", "HTTPS://example.com/path")
	requireCode(t, schema.Validate(context.Background(), "//example.com/path"), "string.url")
	requireCode(t, schema.Validate(context.Background(), "http://example.com/path"), "string.url")
	wantSchema(t, schema, `{"format":"uri","type":"string"}`)
}

// TestURLPolicyEmptyListsUseDefaultSchemes checks that both nil and allocated
// empty variadic lists keep the HTTP/HTTPS defaults. Unlike an explicit empty
// string entry, an empty list does not replace the default scheme policy.
//
// Example:
//
//	go test -run '^TestURLPolicyEmptyListsUseDefaultSchemes$' .
func TestURLPolicyEmptyListsUseDefaultSchemes(t *testing.T) {
	t.Parallel()

	for _, schemes := range [][]string{nil, {}} {
		schema := validate.String(func(text *validate.StringBuilder[string]) {
			text.URL(schemes...)
		})
		requireCode(t, schema.Validate(context.Background(), "http://example.com"), "")
		requireCode(t, schema.Validate(context.Background(), "https://example.com"), "")
		requireCode(t, schema.Validate(context.Background(), "ftp://example.com"), "string.url")
		requireCode(t, schema.Validate(context.Background(), "//example.com"), "string.url")
	}
}
