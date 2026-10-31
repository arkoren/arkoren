package broadcast

import (
	"errors"
	"fmt"
	"strings"

	"arkoren.dev/core/internal/name"
)

// ErrInvalidName reports invalid broadcast names or patterns.
// Validation does not normalize case, whitespace, or wildcard placement.
//
//	err := broadcast.ValidateName("Bad Name")
//	fmt.Println(errors.Is(err, broadcast.ErrInvalidName)) // true
var ErrInvalidName = errors.New("invalid broadcast name")

// Topic names one broadcast fact and fixes the payload type T carried by
// it.
// Construction retains literal text; publication and listener registration
// validate it. Topics sharing a name must agree on their payload encoding.
//
//	topic := broadcast.NewTopic[int]("orders.created")
//	fmt.Println(topic.Name())
type Topic[T any] struct {
	// name is the exact broadcast name and routing key.
	name string
}

// NewTopic returns the topic called topicName that carries payloads of type T.
// The name is validated when the topic is published or listened to.
// Invalid declarations therefore do not panic; use ValidateName for eager
// validation before assembling a subscription or dispatch-only producer.
//
//	topic := broadcast.NewTopic[int]("orders.created")
//	if err := broadcast.ValidateName(topic.Name()); err != nil {
//		return err
//	}
func NewTopic[T any](topicName string) Topic[T] {
	return Topic[T]{
		name: topicName,
	}
}

// Name returns the broadcast name of the topic.
// The zero Topic returns empty text, which publication and registration reject.
//
//	fmt.Println(broadcast.NewTopic[int]("orders.created").Name())
func (topic Topic[T]) Name() string {
	return topic.name
}

// ValidateName validates an exact broadcast name: dot-separated tokens, each
// 1-64 lowercase letters, digits, or hyphens with no hyphen at either end, at
// most 255 bytes in total. The error matches [ErrInvalidName].
// Exact names cannot contain wildcards; no input is modified.
//
//	if err := broadcast.ValidateName("orders.created"); err != nil {
//		return err
//	}
func ValidateName(topicName string) error {
	return validate(topicName, false)
}

// ValidatePattern validates a receiver pattern. It follows the rules of
// [ValidateName], and additionally allows the token "*" for any one token and
// the token "#" for the remaining tokens, the latter only as the last token.
// The error matches [ErrInvalidName].
// No input is modified; whitespace and empty tokens are rejected.
//
//	if err := broadcast.ValidatePattern("orders.*.#"); err != nil {
//		return err
//	}
func ValidatePattern(pattern string) error {
	return validate(pattern, true)
}

// validate adapts [name.Routable] to [ErrInvalidName].
func validate(value string, pattern bool) error {
	if err := name.Routable(value, pattern); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidName, err)
	}

	return nil
}

// Match reports whether topicName matches pattern.
// Invalid input returns false. A final # matches zero or more remaining tokens;
// * matches exactly one. Matching is case-sensitive with no normalization.
//
//	fmt.Println(broadcast.Match("orders.*", "orders.created")) // true
//	fmt.Println(broadcast.Match("orders.#", "orders")) // true
func Match(pattern, topicName string) bool {
	if ValidatePattern(pattern) != nil || ValidateName(topicName) != nil {
		return false
	}

	patternTokens := strings.Split(pattern, ".")
	nameTokens := strings.Split(topicName, ".")
	tail := patternTokens[len(patternTokens)-1] == "#"

	if tail {
		patternTokens = patternTokens[:len(patternTokens)-1]
	}

	if len(nameTokens) < len(patternTokens) {
		return false
	}

	if !tail && len(patternTokens) != len(nameTokens) {
		return false
	}

	for index, token := range patternTokens {
		if token != "*" && token != nameTokens[index] {
			return false
		}
	}

	return true
}
