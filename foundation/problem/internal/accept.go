package internal

import (
	"cmp"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// acceptPair stores one parsed media range and its quality value.
type acceptPair struct {
	// media is the parsed media range.
	media string

	// quality is the parsed quality value.
	quality float64

	// parameters contains media requirements, excluding the negotiation weight.
	parameters map[string]string
}

// representationMatch stores a supported representation's client preference.
type representationMatch struct {
	// media is the server-supported representation.
	media string

	// quality is the selected client quality value.
	quality float64

	// headerIndex preserves client ordering for equal preferences.
	headerIndex int

	// serverIndex preserves server ordering for remaining ties.
	serverIndex int

	// specificity ranks exact, type-wildcard, and full-wildcard ranges.
	specificity int

	// parameterCount breaks equal media-range specificity before quality.
	parameterCount int
}

// Accept represents a parsed HTTP Accept header for content negotiation.
// It retains valid media requirements and weights in client order without
// modifying requests. Order compares those requirements with full supported
// content types, including parameters; it performs no response I/O.
type Accept struct {
	// values preserves valid client media ranges in header order.
	values []acceptPair
}

// ParseAccept parses the Accept header values from request.
// Invalid ranges are ignored; missing or wholly invalid headers let Order
// retain the server's supported order. Parsing does not modify the request.
//
// Example:
//
//	request := httptest.NewRequest(http.MethodGet, "/", nil)
//	request.Header.Set("Accept", "application/json")
//	preferred := internal.ParseAccept(request).Order([]string{"text/plain", "application/json"})
//	_ = preferred[0] // "application/json"
func ParseAccept(request *http.Request) Accept {
	accept := Accept{
		values: make([]acceptPair, 0),
	}

	for _, header := range request.Header.Values("Accept") {
		for line := range strings.SplitSeq(header, ",") {
			pair, valid := parseAcceptLine(line)

			if valid {
				accept.values = append(accept.values, pair)
			}
		}
	}

	return accept
}

// parseAcceptLine parses one comma-delimited media range.
// Invalid media ranges return false; valid ranges retain their parsed quality
// so representation selection can compare them without parsing again.
func parseAcceptLine(line string) (acceptPair, bool) {
	media, parameters, err := mime.ParseMediaType(line)

	if err != nil || !validMediaRange(media) {
		return acceptPair{}, false
	}

	quality, valid := parseAcceptQuality(parameters)

	if !valid {
		return acceptPair{}, false
	}

	delete(parameters, "q")

	return acceptPair{
		media:      media,
		quality:    quality,
		parameters: parameters,
	}, true
}

// validMediaRange reports whether media is an exact or wildcard media range.
func validMediaRange(media string) bool {
	if media == "*/*" {
		return true
	}

	mediaType, subtype, found := strings.Cut(media, "/")

	if !found {
		return false
	}

	return validMediaSubtype(mediaType, subtype)
}

// validMediaSubtype reports whether mediaType and subtype form a valid range.
func validMediaSubtype(mediaType, subtype string) bool {
	if mediaType == "*" {
		return false
	}

	if subtype == "*" {
		return true
	}

	return !strings.Contains(subtype, "*")
}

// parseAcceptQuality accepts HTTP's 0–1 quality grammar with at most three
// decimal places. Only omission defaults to one; malformed weights invalidate
// their range rather than gaining maximum preference.
func parseAcceptQuality(parameters map[string]string) (float64, bool) {
	quality, found := parameters["q"]

	if !found {
		return 1, true
	}

	whole, fraction, _ := strings.Cut(quality, ".")

	if whole != "0" && whole != "1" {
		return 0, false
	}

	if len(fraction) > 3 || strings.Trim(fraction, "0123456789") != "" {
		return 0, false
	}

	if whole == "1" && strings.Trim(fraction, "0") != "" {
		return 0, false
	}

	parsed, err := strconv.ParseFloat(quality, 64)

	return parsed, err == nil
}

// Order returns acceptable supported content types in client preference order.
// Supported values may include parameters; results preserve their original
// strings. Missing or wholly invalid Accept ranges keep server order. Matching
// parameters increase specificity; charset values compare case-insensitively.
//
// Example:
//
//	request := httptest.NewRequest(http.MethodGet, "/", nil)
//	request.Header.Set("Accept", "text/plain;charset=UTF-8")
//	preferred := internal.ParseAccept(request).Order([]string{"text/plain; charset=utf-8"})
//	_ = preferred[0]
func (accept Accept) Order(supported []string) []string {
	if len(accept.values) == 0 {
		return slices.Clone(supported)
	}

	matches := accept.acceptableMatches(supported)
	slices.SortFunc(matches, compareRepresentationMatches)

	ordered := make([]string, len(matches))

	for index, match := range matches {
		ordered[index] = match.media
	}

	return ordered
}

// acceptableMatches returns supported representations not excluded by clients.
func (accept Accept) acceptableMatches(supported []string) []representationMatch {
	matches := make([]representationMatch, 0, len(supported))

	for serverIndex, media := range supported {
		match, found := accept.bestMatch(media)

		if !found {
			continue
		}

		if match.quality == 0 {
			continue
		}

		match.serverIndex = serverIndex
		matches = append(matches, match)
	}

	return matches
}

// bestMatch returns the highest-preference client range matching media.
func (accept Accept) bestMatch(media string) (representationMatch, bool) {
	matches := accept.matching(media)

	if len(matches) == 0 {
		return representationMatch{}, false
	}

	best := matches[0]

	for _, candidate := range matches[1:] {
		if representationPreferred(candidate, best) {
			best = candidate
		}
	}

	return best, true
}

// matching returns client ranges that match media with their specificity.
func (accept Accept) matching(media string) []representationMatch {
	mediaType, parameters, err := mime.ParseMediaType(media)

	if err != nil {
		return nil
	}

	matches := make([]representationMatch, 0, len(accept.values))

	for headerIndex, pair := range accept.values {
		specificity := matchSpecificity(mediaType, pair.media)

		if specificity < 0 || !matchingParameters(parameters, pair.parameters) {
			continue
		}

		matches = append(matches, representationMatch{
			media:          media,
			quality:        pair.quality,
			headerIndex:    headerIndex,
			specificity:    specificity,
			parameterCount: len(pair.parameters),
		})
	}

	return matches
}

// matchingParameters requires every client media parameter to be present on
// the actual representation. Charset names are case-insensitive; other values
// retain MIME's exact-value comparison. Empty requirements match any parameters.
func matchingParameters(supported, required map[string]string) bool {
	for name, value := range required {
		actual, found := supported[name]

		if name == "charset" {
			actual, value = strings.ToLower(actual), strings.ToLower(value)
		}

		if !found || actual != value {
			return false
		}
	}

	return true
}

// representationPreferred reports whether candidate outranks current.
func representationPreferred(candidate, current representationMatch) bool {
	if candidate.specificity != current.specificity {
		return candidate.specificity > current.specificity
	}

	if candidate.parameterCount != current.parameterCount {
		return candidate.parameterCount > current.parameterCount
	}

	return candidate.quality > current.quality
}

// matchSpecificity ranks the client media range that matches media.
func matchSpecificity(media, mediaRange string) int {
	if media == mediaRange {
		return 2
	}

	if mediaRange == "*/*" {
		return 0
	}

	if !strings.HasSuffix(mediaRange, "/*") {
		return -1
	}

	if strings.HasPrefix(media, strings.TrimSuffix(mediaRange, "*")) {
		return 1
	}

	return -1
}

// compareRepresentationMatches orders client preference and deterministic ties.
func compareRepresentationMatches(left, right representationMatch) int {
	if left.quality != right.quality {
		return cmp.Compare(right.quality, left.quality)
	}

	if left.headerIndex != right.headerIndex {
		return cmp.Compare(left.headerIndex, right.headerIndex)
	}

	return cmp.Compare(left.serverIndex, right.serverIndex)
}
