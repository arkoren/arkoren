package validate

import (
	"context"
	"net/mail"
	"net/netip"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// StringSchema validates strings and named string types. Create one with
// [String]. Lengths count runes, not bytes. Configure StringBuilder during
// construction; the published schema exposes execution and inspection only.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Trim().Required()
//	})
//	value, err := schema.Parse(context.Background(), " Ada ")
//	if err != nil {
//		panic(err)
//	}
//	fmt.Println(value)
type StringSchema[S ~string] struct {
	// base retains shared immutable rule state for this concrete schema role.
	base[S]
}

// rtype returns the exact validated Go type. Compilation uses it to reject
// mismatched field registrations without converting the input.
func (schema StringSchema[S]) rtype() reflect.Type {
	return reflect.TypeFor[S]()
}

// compile binds this schema's recorded rules and children to an execution
// node. Construction problems are collected in rep before publication.
func (schema StringSchema[S]) compile(e *env, rep *report, where string) *node {
	typ := reflect.TypeFor[S]()
	n := schema.build(e, rep, where, leafHooks[S]{
		typ: typ,
		get: func(v reflect.Value) S {
			return S(v.String())
		},
	})

	schema.verifyDefault(n, rep, where)

	return n
}

// Trim removes leading and trailing Unicode white space from the value
// before anything else: before the presence check, before Default and
// before every rule, wherever Trim appears in the chain. A value that is
// only white space becomes "" and is absent, so Required fails and Default
// applies. Conditions and field comparisons on sibling fields see the
// trimmed value, like they see defaults. Parse returns the trimmed value;
// Validate checks it but returns nothing, so both agree. The input is
// never modified. Trim cannot be used on Map key schemas. Like every
// normalizer it runs in the order written among the other normalizers.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Trim()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Trim() *StringBuilder[S] {
	text.base = text.normalize(func(v S) S {
		return S(strings.TrimSpace(string(v)))
	})

	return text
}

// ToLower lower-cases the whole string. It is a normalizer, see Trim.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.ToLower()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) ToLower() *StringBuilder[S] {
	text.base = text.normalize(func(v S) S {
		return S(strings.ToLower(string(v)))
	})

	return text
}

// ToUpper upper-cases the whole string. It is a normalizer, see Trim.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.ToUpper()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) ToUpper() *StringBuilder[S] {
	text.base = text.normalize(func(v S) S {
		return S(strings.ToUpper(string(v)))
	})

	return text
}

// TrimPrefix removes one leading prefix when present. It is a normalizer,
// see Trim.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.TrimPrefix("prefix-")
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) TrimPrefix(prefix S) *StringBuilder[S] {
	text.base = text.normalize(func(v S) S {
		return S(strings.TrimPrefix(string(v), string(prefix)))
	})

	return text
}

// TrimSuffix removes one trailing suffix when present. It is a
// normalizer, see Trim.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.TrimSuffix("-suffix")
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) TrimSuffix(suffix S) *StringBuilder[S] {
	text.base = text.normalize(func(v S) S {
		return S(strings.TrimSuffix(string(v), string(suffix)))
	})

	return text
}

// Truncate cuts the string to at most n runes. It panics with
// [ErrInvalidSchema] when n is negative. It is a normalizer, see Trim.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Truncate(80)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Truncate(n int) *StringBuilder[S] {
	if n < 0 {
		invalidf("Truncate(%d): length is negative", n)
	}

	text.base = text.normalize(func(v S) S {
		count := 0

		for index := range string(v) {
			if count == n {
				return v[:index]
			}

			count++
		}

		return v
	})

	return text
}

// Validate checks the input using the same preparation and rules as Parse,
// without returning the prepared value. It returns nil, [Errors], or a custom
// Check error; caller-owned input is not modified.
//
// Example:
//
//	schema := validate.String[string]()
//	if err := schema.Validate(context.Background(), "Ada"); err != nil {
//		panic(err)
//	}
func (schema StringSchema[S]) Validate(ctx context.Context, v S) error {
	_, err := schema.Parse(ctx, v)

	return err
}

// Parse checks the input and returns its normalized, defaulted result. It
// copies storage before applying schema changes and returns the zero value
// with [Errors] or a custom Check error when validation fails.
//
// Example:
//
//	schema := validate.String[string]()
//	value, err := schema.Parse(context.Background(), "Ada")
//	if err != nil {
//		panic(err)
//	}
//	_ = value
func (schema StringSchema[S]) Parse(ctx context.Context, v S) (S, error) {
	return execute(ctx, compileStandalone[S](schema), v)
}

// OneOf accepts only the listed values.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.OneOf("free", "pro")
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) OneOf(values ...S) *StringBuilder[S] {
	text.base = text.addRule(oneOfRule(values, true)).annotate("enum", slices.Clone(values))

	return text
}

// NotOneOf rejects the listed values.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.NotOneOf("reserved", "admin")
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) NotOneOf(values ...S) *StringBuilder[S] {
	text.base = text.addRule(oneOfRule(values, false))

	return text
}

// Min requires at least n runes. A negative n records a construction diagnostic;
// Struct reports it with other registration errors, standalone execution panics
// with ErrInvalidSchema, and JSONSchema returns an error wrapping that sentinel.
// Later length settings do not clear the diagnostic. Zero is a valid bound.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Min(3)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Min(n int) *StringBuilder[S] {
	if n < 0 {
		text.base = text.problemf("Min(%d): length is negative", n)

		return text
	}

	text.base = text.addRule(func(_ context.Context, v S) error {
		if utf8.RuneCountInString(string(v)) >= n {
			return nil
		}

		return Fail("string.min", map[string]any{"min": n})
	}).annotate("minLength", n)

	return text
}

// Max allows at most n runes. A negative n records a construction diagnostic
// with the same reporting and sticky-error behavior as Min. Zero is valid and
// rejects every present nonempty string; ordinary absence still skips rules.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Max(80)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Max(n int) *StringBuilder[S] {
	if n < 0 {
		text.base = text.problemf("Max(%d): length is negative", n)

		return text
	}

	text.base = text.addRule(func(_ context.Context, v S) error {
		if utf8.RuneCountInString(string(v)) <= n {
			return nil
		}

		return Fail("string.max", map[string]any{"max": n})
	}).annotate("maxLength", n)

	return text
}

// Len requires exactly n runes. A negative n records a construction diagnostic
// with the same reporting and sticky-error behavior as Min. Zero is valid;
// use Required separately when empty input must fail the presence check.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Len(2)
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Len(n int) *StringBuilder[S] {
	if n < 0 {
		text.base = text.problemf("Len(%d): length is negative", n)

		return text
	}

	text.base = text.addRule(func(_ context.Context, v S) error {
		if utf8.RuneCountInString(string(v)) == n {
			return nil
		}

		return Fail("string.len", map[string]any{"len": n})
	}).annotate("minLength", n).annotate("maxLength", n)

	return text
}

// NotBlank rejects strings that contain only white space.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.NotBlank()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) NotBlank() *StringBuilder[S] {
	return text.format("string.not_blank", func(v string) bool {
		return strings.TrimSpace(v) != ""
	})
}

// Regex requires the string to match pattern.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Regex(regexp.MustCompile(`^[a-z]+$`))
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Regex(pattern *regexp.Regexp) *StringBuilder[S] {
	if pattern == nil {
		invalidf("Regex pattern is nil")
	}

	text.base = text.addRule(func(_ context.Context, v S) error {
		if pattern.MatchString(string(v)) {
			return nil
		}

		return Fail("string.regex", map[string]any{"pattern": pattern.String()})
	}).annotate("pattern", pattern.String())

	return text
}

// Prefix requires the string to start with prefix.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Prefix("https://")
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Prefix(prefix string) *StringBuilder[S] {
	text.base = text.addRule(func(_ context.Context, v S) error {
		if strings.HasPrefix(string(v), prefix) {
			return nil
		}

		return Fail("string.prefix", map[string]any{"prefix": prefix})
	})

	return text
}

// Suffix requires the string to end with suffix.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Suffix(".pdf")
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Suffix(suffix string) *StringBuilder[S] {
	text.base = text.addRule(func(_ context.Context, v S) error {
		if strings.HasSuffix(string(v), suffix) {
			return nil
		}

		return Fail("string.suffix", map[string]any{"suffix": suffix})
	})

	return text
}

// Contains requires the string to contain substring.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Contains("@")
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Contains(substring string) *StringBuilder[S] {
	text.base = text.addRule(func(_ context.Context, v S) error {
		if strings.Contains(string(v), substring) {
			return nil
		}

		return Fail("string.contains", map[string]any{"substring": substring})
	})

	return text
}

// Email requires a bare email address such as "ada@example.com". Display
// names and comments are rejected.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Email()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Email() *StringBuilder[S] {
	text.format("string.email", validEmail)
	text.base = text.annotate("format", "email")

	return text
}

// URL requires an absolute URL with a host and one of the allowed schemes,
// compared case-insensitively. Without arguments only "http" and "https" are
// allowed, so values rendered as links cannot carry "javascript:", "data:",
// or other active schemes. Pass schemes to widen the set, for example
// URL("https", "ftp"); the argument list replaces the default rather than
// adding to it. URL copies the list when called, so later changes to the
// supplied slice cannot change the rule. An empty scheme entry matches no
// absolute URL; it never permits scheme-relative references such as
// "//example.com/path". An empty list still selects the defaults.
//
// Failures use the code "string.url". Like other ordinary rules, URL skips
// absent strings; add Required to reject empty input. JSONSchema exports
// format "uri", not the scheme restriction or host requirement.
//
// Example:
//
//	schemes := []string{"https", "ftp"}
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.URL(schemes...)
//	})
//	schemes[0] = "http" // The schema still allows only https and ftp.
//	if err := schema.Validate(context.Background(), "https://example.com/file"); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) URL(schemes ...string) *StringBuilder[S] {
	schemes = slices.Clone(schemes)

	if len(schemes) == 0 {
		schemes = []string{"http", "https"}
	}

	text.format("string.url", func(v string) bool {
		return validURL(v, schemes)
	})
	text.base = text.annotate("format", "uri")

	return text
}

// UUID requires the canonical 8-4-4-4-12 hexadecimal form of any version.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.UUID()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) UUID() *StringBuilder[S] {
	text.format("string.uuid", validUUID)
	text.base = text.annotate("format", "uuid")

	return text
}

// Duration requires text that [time.ParseDuration] accepts, such as
// "182ms", "1.5h" or "-3s". It is the check for a duration received as a
// string; use [Text] with [Duration] for a field that already holds one.
// Failures use the code "string.duration".
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Duration()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Duration() *StringBuilder[S] {
	text.format("string.duration", validDuration)
	text.base = text.annotate("format", goDurationFormat)

	return text
}

// Domain requires a domain name with at least two labels and an
// alphabetic top-level domain.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Domain()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Domain() *StringBuilder[S] {
	return text.format("string.domain", validDomain)
}

// IP requires an IPv4 or IPv6 address without a zone.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.IP()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) IP() *StringBuilder[S] {
	return text.format("string.ip", func(v string) bool {
		address, err := netip.ParseAddr(v)

		return err == nil && address.Zone() == ""
	})
}

// Lowercase requires the string to equal its lower case form.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Lowercase()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Lowercase() *StringBuilder[S] {
	return text.format("string.lowercase", func(v string) bool {
		return v == strings.ToLower(v)
	})
}

// Uppercase requires the string to equal its upper case form.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Uppercase()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Uppercase() *StringBuilder[S] {
	return text.format("string.uppercase", func(v string) bool {
		return v == strings.ToUpper(v)
	})
}

// Alpha requires ASCII letters only.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.Alpha()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) Alpha() *StringBuilder[S] {
	return text.format("string.alpha", func(v string) bool {
		return onlyASCII(v, "")
	})
}

// AlphaNumeric requires ASCII letters and digits only.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.AlphaNumeric()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) AlphaNumeric() *StringBuilder[S] {
	return text.format("string.alphanumeric", func(v string) bool {
		return onlyASCII(v, "0123456789")
	})
}

// AlphaDash requires ASCII letters, digits, hyphens and underscores only.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.AlphaDash()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) AlphaDash() *StringBuilder[S] {
	return text.format("string.alpha_dash", func(v string) bool {
		return onlyASCII(v, "0123456789-_")
	})
}

// ASCII requires every character to be ASCII.
//
// Example:
//
//	schema := validate.String(func(text *validate.StringBuilder[string]) {
//		text.ASCII()
//	})
//	if _, err := schema.JSONSchema(); err != nil {
//		panic(err)
//	}
func (text *StringBuilder[S]) ASCII() *StringBuilder[S] {
	return text.format("string.ascii", func(v string) bool {
		for index := range len(v) {
			if v[index] > unicode.MaxASCII {
				return false
			}
		}

		return true
	})
}

// format adds a rule that fails with code when valid returns false.
func (text *StringBuilder[S]) format(code string, valid func(string) bool) *StringBuilder[S] {
	text.base = text.addRule(func(_ context.Context, v S) error {
		if valid(string(v)) {
			return nil
		}

		return Fail(code, nil)
	})

	return text
}

// onlyASCII reports whether v contains only ASCII letters and the extra
// characters.
func onlyASCII(v, extra string) bool {
	for index := range len(v) {
		char := v[index]
		letter := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')

		if !letter && !strings.ContainsRune(extra, rune(char)) {
			return false
		}
	}

	return true
}

// validEmail reports whether v is a bare RFC 5322 address.
func validEmail(v string) bool {
	address, err := mail.ParseAddress(v)

	return err == nil && address.Address == v
}

// validURL reports whether v parses as an absolute URL with a host and a
// scheme in schemes, compared case-insensitively. Malformed and relative
// references fail independently of the allowlist, including empty entries.
func validURL(v string, schemes []string) bool {
	parsed, err := url.Parse(v)

	if err != nil || parsed.Host == "" {
		return false
	}

	if !parsed.IsAbs() {
		return false
	}

	return slices.ContainsFunc(schemes, func(scheme string) bool {
		return strings.EqualFold(scheme, parsed.Scheme)
	})
}

// validDuration reports whether v is text that time.ParseDuration accepts.
func validDuration(v string) bool {
	_, err := time.ParseDuration(v)

	return err == nil
}

// validUUID checks the 36-byte dashed UUID syntax with case-insensitive ASCII
// hexadecimal digits. It checks textual shape, not version or variant bits.
// Matching the alphabet directly avoids a separate numeric range for each case.
func validUUID(v string) bool {
	if len(v) != 36 {
		return false
	}

	for index := range len(v) {
		char := v[index]

		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", rune(char)) {
				return false
			}
		}
	}

	return true
}

// validDomain checks ASCII DNS label syntax and an alphabetic top-level domain
// of at least two characters. Labels are at most 63 bytes and the whole name at
// most 253 bytes. The final alphabet check also rejects top-level hyphens, so
// label validation need not special-case the last label during traversal.
func validDomain(v string) bool {
	labels := strings.Split(v, ".")

	if len(labels) < 2 || len(v) > 253 {
		return false
	}

	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}

		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}

		if !onlyASCII(label, "0123456789-") {
			return false
		}
	}

	last := labels[len(labels)-1]

	return len(last) >= 2 && onlyASCII(last, "")
}
