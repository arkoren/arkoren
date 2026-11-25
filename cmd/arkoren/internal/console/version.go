package console

import (
	"runtime/debug"
)

// VersionInfo returns the CLI's own version: the module version it was built
// at, or "(devel)" for a local build.
func VersionInfo() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}

	return "(devel)"
}

// Version is the JSON document of `arkoren --version`: plain data without a
// summary. Only Version is present unless verbose output was requested.
type Version struct {
	// Version is the module version, or "(devel)" for a local build.
	Version string `json:"version"`

	// Module is the main module path; verbose only.
	Module string `json:"module,omitzero"`

	// GoVersion is the toolchain that built the CLI; verbose only.
	GoVersion string `json:"go_version,omitzero"`

	// Revision is the VCS commit the CLI was built from; verbose only.
	Revision string `json:"revision,omitzero"`

	// RevisionAt is the commit time; verbose only.
	RevisionAt string `json:"revision_at,omitzero"`

	// Modified reports uncommitted changes at build time; verbose only, and
	// absent when the build recorded no VCS state.
	Modified *bool `json:"modified,omitzero"`
}

// VersionReport adds only safe build identifiers in verbose mode. Compiler
// flags, dependency inventories, and local replacement paths are not exposed.
func VersionReport(verbose bool) Version {
	report := Version{Version: VersionInfo()}

	if !verbose {
		return report
	}

	info, ok := debug.ReadBuildInfo()

	if !ok {
		return report
	}

	report.GoVersion = info.GoVersion
	report.Module = info.Main.Path

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			report.Revision = setting.Value
		case "vcs.time":
			report.RevisionAt = setting.Value
		case "vcs.modified":
			modified := setting.Value == "true"
			report.Modified = &modified
		}
	}

	return report
}
