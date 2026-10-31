// Package negotiation resolves original catalog keys from HTTP preferences.
package negotiation

import (
	"cmp"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Resolver holds immutable normalized matching indexes, not translation maps.
type Resolver struct {
	// query names the highest-precedence selector, or disables it when empty.
	query string
	// cookie names the second selector, or disables it when empty.
	cookie string
	// supported maps normalized tags to their original catalog keys.
	supported map[string]string
	// fallback is the original key returned when no selector matches.
	fallback string
}

// New validates completed tag and cookie settings before indexing original keys.
func New(fallback string, keys iter.Seq[string], query, cookie string) Resolver {
	fallback = cmp.Or(fallback, "en")

	if !validLanguageTag(fallback) {
		panic(fmt.Sprintf("locale: invalid default language tag %q", fallback))
	}

	if cookie != "" {
		if err := (&http.Cookie{Name: cookie}).Valid(); err != nil {
			panic(fmt.Sprintf("locale: invalid cookie name: %v", err))
		}
	}

	supported := make(map[string]string)

	for key := range keys {
		if !validLanguageTag(key) {
			panic(fmt.Sprintf("locale: invalid language tag %q", key))
		}

		tag := normalize(key)

		if _, exists := supported[tag]; exists {
			panic(fmt.Sprintf("locale: ambiguous language tag %q", key))
		}

		supported[tag] = key
	}

	if key, ok := supported[normalize(fallback)]; ok {
		fallback = key
	}

	return Resolver{query: query, cookie: cookie, supported: supported, fallback: fallback}
}

// Resolve tries query, cookie, and ordered header preferences before fallback.
func (resolver Resolver) Resolve(request *http.Request) string {
	if name := resolver.query; name != "" {
		if tag, ok := resolver.match(request.URL.Query().Get(name)); ok {
			return tag
		}
	}

	if name := resolver.cookie; name != "" {
		if cookie, err := request.Cookie(name); err == nil {
			if tag, ok := resolver.match(cookie.Value); ok {
				return tag
			}
		}
	}

	for _, candidate := range acceptedLanguages(request.Header.Get("Accept-Language")) {
		if tag, ok := resolver.match(candidate); ok {
			return tag
		}
	}

	return resolver.fallback
}

// match resolves exact normalized tags before their base language.
func (resolver Resolver) match(tag string) (string, bool) {
	tag = normalize(tag)

	if tag == "" {
		return "", false
	}

	if key, ok := resolver.supported[tag]; ok {
		return key, true
	}

	key, ok := resolver.supported[baseLanguage(tag)]

	return key, ok
}

// preference retains header order when equal quality values are sorted.
type preference struct {
	// tag retains the client spelling until matching.
	tag string
	// quality is a valid HTTP q-value in (0, 1].
	quality float64
}

// acceptedLanguages orders valid ranges stably by descending quality.
func acceptedLanguages(value string) []string {
	var preferences []preference

	for part := range strings.SplitSeq(value, ",") {
		tag, params, _ := strings.Cut(part, ";")
		tag = strings.TrimSpace(tag)

		if !validLanguageTag(tag) {
			continue
		}

		quality, ok := parseQuality(params)

		if !ok || quality <= 0 {
			continue
		}

		preferences = append(preferences, preference{tag: tag, quality: quality})
	}

	slices.SortStableFunc(preferences, func(left, right preference) int {
		return cmp.Compare(right.quality, left.quality)
	})

	tags := make([]string, len(preferences))

	for index, entry := range preferences {
		tags[index] = entry.tag
	}

	return tags
}

// parseQuality rejects missing or duplicate q parameters and defaults to one.
func parseQuality(params string) (float64, bool) {
	quality := 1.0
	qualitySet := false

	for param := range strings.SplitSeq(params, ";") {
		name, value, found := strings.Cut(param, "=")

		if !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}

		if !found || qualitySet {
			return 0, false
		}

		parsed, ok := parseWeight(strings.TrimSpace(value))

		if !ok {
			return 0, false
		}

		quality = parsed
		qualitySet = true
	}

	return quality, true
}

// parseWeight accepts HTTP qvalue syntax, not general floating-point syntax.
func parseWeight(value string) (float64, bool) {
	whole, fraction, _ := strings.Cut(value, ".")

	if whole != "0" && whole != "1" {
		return 0, false
	}

	if len(fraction) > 3 || strings.Trim(fraction, "0123456789") != "" {
		return 0, false
	}

	if whole == "1" && strings.Trim(fraction, "0") != "" {
		return 0, false
	}

	quality, err := strconv.ParseFloat(value, 64)

	return quality, err == nil
}

// validLanguageTag accepts the existing ASCII tag shape and normalization.
func validLanguageTag(tag string) bool {
	const letters = "abcdefghijklmnopqrstuvwxyz"

	if strings.IndexFunc(tag, func(character rune) bool {
		return character > unicode.MaxASCII
	}) >= 0 {
		return false
	}

	parts := strings.Split(normalize(tag), "-")

	if !validSubtag(parts[0], letters) {
		return false
	}

	for _, part := range parts[1:] {
		if !validSubtag(part, letters+"0123456789") {
			return false
		}
	}

	return true
}

// validSubtag checks the permitted alphabet and one-to-eight-character bound.
func validSubtag(part, alphabet string) bool {
	return len(part) > 0 && len(part) <= 8 && strings.Trim(part, alphabet) == ""
}

// normalize lowercases matching keys without changing the original catalog key.
func normalize(tag string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
}

// baseLanguage selects the first component of a normalized tag.
func baseLanguage(tag string) string {
	base, _, _ := strings.Cut(tag, "-")

	return base
}
