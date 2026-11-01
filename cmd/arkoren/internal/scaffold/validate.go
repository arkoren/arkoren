package scaffold

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"arkoren.dev/cmd/arkoren/internal/console"
	"arkoren.dev/cmd/arkoren/internal/files"
)

// newElementPattern limits generated directory and import elements to Go's
// portable module-path alphabet. Quotes and template/source injection cannot
// enter either an import string or a go.mod directive through these values.
var newElementPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// newReservedPattern rejects Windows device names, including names with a
// suffix, so a scaffold has the same file semantics on supported platforms.
var newReservedPattern = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])$`)

// newShortPattern detects Windows abbreviated names in the pre-dot stem,
// which Go rejects because they can alias other module cache directories.
var newShortPattern = regexp.MustCompile(`~[0-9]+$`)

// newDomainPattern permits Go's lowercase host alphabet. A single local
// module name need not be a remotely resolvable host.
var newDomainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// newMajorPattern identifies semantic-import-version suffixes. A v1 or a
// zero-padded major is invalid under Go's module path rules.
var newMajorPattern = regexp.MustCompile(`^v[0-9.]+$`)

// newGopkgPattern accepts gopkg.in's alternative dotted major suffix, including
// its supported unstable spelling, without requiring network lookup.
var newGopkgPattern = regexp.MustCompile(`\.v(0|[1-9][0-9]*)(-unstable)?$`)

// newPlanConflicts checks the complete rendered inventory before filesystem
// inspection. It reports duplicate files and files also needed as directories,
// regardless of emission order. Paths are trusted project-relative native paths.
// Case folding keeps plans portable to case-insensitive filesystems without
// probing or writing the destination. Shared directories and filename prefixes
// remain valid; only complete path components participate in ancestry checks.
// Results are sorted and deduplicated without changing the caller's inventory.
func newPlanConflicts(inventory []files.File) []string {
	// planned indexes normalized names while retaining the original spelling for
	// diagnostics. A complete index makes ancestry checks independent of order.
	planned := make(map[string]string, len(inventory))
	conflicts := make([]string, 0)

	for _, file := range inventory {
		path := strings.ToLower(filepath.Clean(file.Path))

		if previous, ok := planned[path]; ok {
			conflicts = append(conflicts, fmt.Sprintf("%s: generated path collides with %s", file.Path, previous))
			continue
		}

		planned[path] = file.Path
	}

	for path := range planned {
		if ancestor := newPlanFileAncestor(path, planned); ancestor != "" {
			conflicts = append(conflicts, ancestor+": generated file is also required as a directory")
		}
	}

	slices.Sort(conflicts)

	return slices.Compact(conflicts)
}

// newPlanFileAncestor finds the nearest ancestor that is itself a planned file.
// path and the index keys must already be cleaned and case-folded; the returned
// spelling comes from the inventory for diagnostics. The project root is not a
// generated file, so reaching it means no ancestor conflict and returns empty.
func newPlanFileAncestor(path string, planned map[string]string) string {
	for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
		if file, ok := planned[parent]; ok {
			return file
		}
	}

	return ""
}

// validateNewElement checks one portable import/destination element. A plain
// basename such as hello is allowed, matching go mod init's local-module use.
func validateNewElement(element string) error {
	if !newElementPattern.MatchString(element) || strings.HasPrefix(element, ".") {
		return fmt.Errorf("invalid path element %q", element)
	}

	if strings.HasSuffix(element, ".") || strings.Contains(element, "..") {
		return fmt.Errorf("invalid path element %q", element)
	}

	stem, _, _ := strings.Cut(element, ".")

	if newReservedPattern.MatchString(stem) || newShortPattern.MatchString(stem) {
		return fmt.Errorf("nonportable path element %q", element)
	}

	return nil
}

// ValidateNewModule validates Go module declaration/import syntax using only
// the standard library. Local single-element modules are intentional because
// the default module is the destination basename; remote roots must be lower
// case host-like names. No network resolution is attempted.
func ValidateNewModule(module string) error {
	parts := strings.Split(module, "/")

	for _, part := range parts {
		if err := validateNewElement(part); err != nil {
			return console.UsageError("--module: %v", err)
		}
	}

	first := parts[0]

	if strings.HasPrefix(first, "-") {
		return console.UsageError("--module: leading dash is not allowed")
	}

	if strings.Contains(first, ".") && !newDomainPattern.MatchString(first) {
		return console.UsageError("--module: domain must be lowercase and contain no underscores or tildes")
	}

	return validateNewMajor(module)
}

// validateNewMajor checks only semantic-import-version suffixes. Ordinary
// names such as v2beta remain plain path elements, not version declarations.
func validateNewMajor(module string) error {
	if strings.HasPrefix(module, "gopkg.in/") {
		if !newGopkgPattern.MatchString(module) {
			return console.UsageError("--module: gopkg.in paths require a .vN suffix")
		}

		return nil
	}

	last := module[strings.LastIndex(module, "/")+1:]

	if !strings.Contains(module, "/") || !newMajorPattern.MatchString(last) {
		return nil
	}

	if last[1] == '0' || last == "v1" || strings.Contains(last, ".") {
		return console.UsageError("--module: invalid major-version suffix %q", last)
	}

	return nil
}
