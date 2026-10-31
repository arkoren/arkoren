package validate

import (
	"time"
)

// Duration is a [time.Duration] that encodes as Go duration text such as
// "182ms" or "1h30m" instead of a nanosecond count.
//
// encoding/json/v2 refuses to marshal a bare time.Duration because it has
// no default representation. Declaring a field as Duration gives it one,
// through MarshalText and UnmarshalText, and the same text is what
// [StringBuilder.Duration] accepts. Describe such a field with
// [Text]: Duration reports the custom "go-duration" format to [Text] and
// [Infer]. The standard JSON Schema "duration" format instead describes the
// incompatible RFC 3339 Appendix A grammar, such as "PT1H30M".
//
// Example:
//
//	type Result struct {
//		Elapsed validate.Duration `json:"elapsed"`
//	}
//
//	out, _ := json.Marshal(Result{
//		Elapsed: validate.Duration(182 * time.Millisecond),
//	})
//	// {"elapsed":"182ms"}
type Duration time.Duration

// MarshalText implements [encoding.TextMarshaler]. It returns the text of
// [time.Duration.String], such as "182ms". It never fails.
//
// Example:
//
//	duration := validate.Duration(time.Second)
//	text, err := duration.MarshalText()
//	if err != nil {
//		panic(err)
//	}
//	_ = text
func (duration Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(duration).String()), nil
}

// UnmarshalText implements [encoding.TextUnmarshaler]. It parses text with
// [time.ParseDuration], so "1.5h" and "-3s" are accepted, and returns that
// function's error for anything else. d is left unchanged on error.
//
// Example:
//
//	duration := validate.Duration(time.Second)
//	if err := duration.UnmarshalText([]byte("250ms")); err != nil {
//		panic(err)
//	}
func (duration *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))

	if err != nil {
		return err
	}

	*duration = Duration(parsed)

	return nil
}

// Std returns the value as a [time.Duration].
//
// Example:
//
//	duration := validate.Duration(time.Second)
//	standard := duration.Std()
//	_ = standard
func (duration Duration) Std() time.Duration {
	return time.Duration(duration)
}

// JSONSchemaFormat returns "go-duration", the custom annotation that [Text]
// and [Infer] render for Go duration text. It deliberately does not claim the
// standard RFC 3339 Appendix A "duration" format. Any type with a
// JSONSchemaFormat method gets the same annotation treatment.
//
// Example:
//
//	duration := validate.Duration(time.Second)
//	format := duration.JSONSchemaFormat()
//	_ = format
func (Duration) JSONSchemaFormat() string {
	return goDurationFormat
}
