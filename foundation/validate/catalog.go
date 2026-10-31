package validate

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"

	"arkoren.dev/foundation/validate/internal/numbers"
)

// Messages maps a violation code to a message template. Templates may
// contain {param} placeholders that are replaced with violation params.
type Messages map[string]string

// Catalog holds message templates per locale.
//
// Locales are keyed by language tag, such as "en" or "es-MX". Default
// names the locale used when the requested one has no message for a code.
type Catalog struct {
	// Default is the locale consulted after the requested locale and its
	// base language.
	Default string

	// Locales maps a language tag to its messages.
	Locales map[string]Messages
}

// English returns a copy of the built-in English messages. Use it as the
// starting point for overrides or for a catalog that adds translations.
func English() Messages {
	return maps.Clone(messagesEnglish)
}

// baseLanguage returns the language part of a tag, so "es-MX" yields
// "es". A tag without a region is returned unchanged.
func baseLanguage(tag string) string {
	if index := strings.IndexAny(tag, "-_"); index >= 0 {
		return tag[:index]
	}

	return tag
}

// lookup finds the template for code using the fallback chain: the
// requested locale, its base language, the catalog default, the built-in
// English messages.
func lookup(ctx context.Context, code string) (string, bool) {
	catalog, _ := ctx.Value(catalogKey).(Catalog)
	locale, _ := LocaleFromContext(ctx)

	candidates := [...]string{locale, baseLanguage(locale), catalog.Default, baseLanguage(catalog.Default)}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}

		if template, ok := catalog.Locales[candidate][code]; ok {
			return template, true
		}
	}

	template, ok := messagesEnglish[code]

	return template, ok
}

// lookupCatalog is lookup without the built-in English fallback.
func lookupCatalog(ctx context.Context, code string) (string, bool) {
	catalog, _ := ctx.Value(catalogKey).(Catalog)
	locale, _ := LocaleFromContext(ctx)

	for _, candidate := range [...]string{locale, baseLanguage(locale), catalog.Default, baseLanguage(catalog.Default)} {
		if candidate == "" {
			continue
		}

		if template, ok := catalog.Locales[candidate][code]; ok {
			return template, true
		}
	}

	return "", false
}

// variantTemplate finds the template for code specialized by the "op"
// param, such as "required_if:eq". A catalog that only translates the
// plain code wins over the built-in variant, so translations stay in one
// language.
func variantTemplate(ctx context.Context, code string, params map[string]any) (string, bool) {
	op, _ := params["op"].(string)

	if op == "" {
		return "", false
	}

	variant := code + ":" + op

	if template, ok := lookupCatalog(ctx, variant); ok {
		return template, true
	}

	if _, ok := lookupCatalog(ctx, code); ok {
		return "", false
	}

	template, ok := messagesEnglish[variant]

	return template, ok
}

// message renders the message for code, falling back to the code itself
// when no catalog knows it.
func message(ctx context.Context, code string, params map[string]any) string {
	if code == "slice.count_where" || code == "map.count_where" {
		params = countMessageParams(ctx, params)
	}

	template, ok := variantTemplate(ctx, code, params)

	if !ok {
		template, ok = lookup(ctx, code)
	}

	if !ok {
		return code
	}

	return interpolate(template, params)
}

// countMessageParams rerenders the numeric constraint when a derived-count
// diagnostic changes locale. It copies parameters instead of mutating stored
// violations and accepts only the pure count rules, preventing recursive codes.
func countMessageParams(ctx context.Context, params map[string]any) map[string]any {
	rule, _ := params["rule"].(string)
	numeric := strings.HasPrefix(rule, "number.") || rule == "one_of" || rule == "not_one_of"

	if !numeric {
		return params
	}

	localized := cloneParams(params)
	localized["constraint"] = message(ctx, rule, params)

	return localized
}

// interpolate replaces {name} placeholders with the matching params.
// Placeholders without a param are left untouched.
func interpolate(template string, params map[string]any) string {
	if !strings.Contains(template, "{") {
		return template
	}

	var builder strings.Builder

	for {
		start := strings.IndexByte(template, '{')

		if start < 0 {
			break
		}

		end := strings.IndexByte(template[start:], '}')

		if end < 0 {
			break
		}

		end += start
		builder.WriteString(template[:start])

		name := template[start+1 : end]

		if value, ok := params[name]; ok {
			builder.WriteString(formatParam(value))
		} else {
			builder.WriteString(template[start : end+1])
		}

		template = template[end+1:]
	}

	builder.WriteString(template)

	return builder.String()
}

// formatParam renders a param for a message. Ordinary slices are joined with
// commas and times use RFC 3339. Raw numeric JSON retains its decimal spelling
// rather than being rendered as the byte values of its owned representation.
func formatParam(value any) string {
	if number, ok := numbers.Text(value); ok {
		return number
	}

	if moment, ok := value.(time.Time); ok {
		return moment.Format(time.RFC3339)
	}

	reflected := reflect.ValueOf(value)

	if reflected.Kind() != reflect.Slice {
		return fmt.Sprint(value)
	}

	parts := make([]string, reflected.Len())

	for index := range parts {
		parts[index] = formatParam(reflected.Index(index).Interface())
	}

	return strings.Join(parts, ", ")
}
