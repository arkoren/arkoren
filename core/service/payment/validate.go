package payment

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// keyPattern is the common provider-safe idempotency syntax, including UUIDs.
// Requiring a key prevents accidental non-idempotent retries by the facade.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,38}$`)

// CheckContext rejects nil contexts and preserves cancellation before dispatch.
// Direct drivers use it, or the request/ID Validate methods which call it, before
// SDK calls and side effects. Nil wraps ErrInvalidRequest; canceled contexts
// return ctx.Err without converting it to an uncertain mutation outcome.
//
// Example:
//
//	if err := payment.CheckContext(ctx); err != nil { return err }
func CheckContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context cannot be nil", ErrInvalidRequest)
	}

	return ctx.Err()
}

// checkID validates context first, then a nonempty opaque identity of at most
// 255 bytes. Whitespace and controls are rejected without exposing the value.
func checkID(ctx context.Context, value, field string) error {
	if err := CheckContext(ctx); err != nil {
		return err
	}

	return checkIdentity(value, field)
}

// checkIdentity is context-free opaque identity validation shared by immutable
// request construction and contextual driver preflight. It never echoes input.
func checkIdentity(value, field string) error {
	if value == "" || len(value) > 255 {
		return fmt.Errorf("%w: %s length", ErrInvalidRequest, field)
	}

	if strings.ContainsFunc(value, unicode.IsSpace) {
		return fmt.Errorf("%w: %s whitespace", ErrInvalidRequest, field)
	}

	return checkText(value, field, 255)
}

// checkText limits optional human-visible text by Unicode runes and rejects
// controls and malformed UTF-8. Error text contains only the field name.
func checkText(value, field string, limit int) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return fmt.Errorf("%w: %s length or encoding", ErrInvalidRequest, field)
	}

	if strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("%w: %s controls", ErrInvalidRequest, field)
	}

	return nil
}

// checkKey enforces the required provider-common idempotency syntax, without
// storing the key or disclosing it in validation errors.
func checkKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("%w: idempotency_key syntax", ErrInvalidRequest)
	}

	return nil
}

// checkPositiveMoney applies the mutation-only positive-amount rule separately
// from Money's nonnegative record and conversion semantics.
func checkPositiveMoney(money Money) error {
	if money.Amount <= 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalidRequest)
	}

	_, err := CurrencyExponent(money.Currency)

	return err
}

// checkCheckoutText validates the shared optional checkout text and required
// mutation key. Description may be empty for subscription checkouts.
func checkCheckoutText(description, reference, key string) error {
	if err := checkText(description, "description", 127); err != nil {
		return err
	}

	if err := checkText(reference, "reference", 127); err != nil {
		return err
	}

	return checkKey(key)
}

// checkRedirects requires a return URL and validates the optional cancellation
// URL with the same policy. It leaves both input strings unchanged.
func checkRedirects(returnURL, cancelURL string) error {
	if err := checkURL(returnURL); err != nil {
		return fmt.Errorf("return_url: %w", err)
	}

	if cancelURL == "" {
		return nil
	}

	return checkURL(cancelURL)
}

// checkURL rejects controls, credentials and fragments before enforcing HTTPS,
// with HTTP permitted only on localhost or numeric loopback addresses. Parsing
// never serializes the URL, preserving literal checkout-session placeholders.
func checkURL(value string) error {
	decoded, err := url.PathUnescape(value)

	if err != nil || !utf8.ValidString(decoded) {
		return fmt.Errorf("%w: redirect encoding", ErrInvalidRequest)
	}

	if strings.ContainsFunc(decoded, unicode.IsControl) || strings.Contains(value, "#") {
		return fmt.Errorf("%w: redirect controls or fragment", ErrInvalidRequest)
	}

	location, err := url.Parse(value)

	if err != nil {
		return fmt.Errorf("%w: redirect syntax", ErrInvalidRequest)
	}

	return checkHostedURL(location)
}

// checkHostedURL applies host, credential, and transport policy to an already
// parsed redirect. Native preparation callbacks share only this hosted branch;
// neither caller parses or normalizes the same URL twice.
func checkHostedURL(location *url.URL) error {
	if location.User != nil || location.Hostname() == "" {
		return fmt.Errorf("%w: redirect host or credentials", ErrInvalidRequest)
	}

	return checkURLScheme(location)
}

// checkURLScheme separates transport policy from URL syntax. Loopback HTTP is
// for local development only; arbitrary hostnames never receive an HTTP fallback.
func checkURLScheme(location *url.URL) error {
	if location.Scheme == "https" {
		return nil
	}

	if location.Scheme != "http" {
		return fmt.Errorf("%w: redirect scheme", ErrInvalidRequest)
	}

	if strings.EqualFold(location.Hostname(), "localhost") {
		return nil
	}

	if address := net.ParseIP(location.Hostname()); address != nil && address.IsLoopback() {
		return nil
	}

	return fmt.Errorf("%w: redirect requires https", ErrInvalidRequest)
}
