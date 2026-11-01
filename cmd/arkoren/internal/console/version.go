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

// VersionReport adds only safe build identifiers in verbose mode. Compiler
// flags, dependency inventories, and local replacement paths are not exposed.
func VersionReport(verbose bool) map[string]any {
	report := map[string]any{"version": VersionInfo()}

	if !verbose {
		return report
	}

	info, ok := debug.ReadBuildInfo()

	if !ok {
		return report
	}

	report["go_version"] = info.GoVersion

	if info.Main.Path != "" {
		report["module"] = info.Main.Path
	}

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			report["revision"] = setting.Value
		case "vcs.time":
			report["revision_at"] = setting.Value
		case "vcs.modified":
			report["modified"] = setting.Value == "true"
		}
	}

	return report
}
