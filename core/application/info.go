package application

import (
	"runtime/debug"
	"time"
)

// InfoPath is the conventional path of [Application.InfoHandler]:
//
//	r.Get(application.InfoPath, app.InfoHandler())
const InfoPath = "/infoz"

// develVersion is the main module version the go command records when it
// knows none.
const develVersion = "(devel)"

// Info describes an application at one moment: the body of
// [Application.InfoHandler] and the result of [Application.Info]. The JSON
// tags are the wire format of the info endpoint.
// The value is a snapshot, not configuration, and can reveal operational details.
//
//	info := app.Info()
//	fmt.Println(info.Name, info.State)
type Info struct {
	// Name is the service name.
	// Example: fmt.Println(app.Info().Name)
	Name string `json:"name,omitzero"`

	// Version is the service version.
	// Example: fmt.Println(app.Info().Version)
	Version string `json:"version,omitzero"`

	// Environment is the deployment environment.
	// Example: fmt.Println(app.Info().Environment == application.Production)
	Environment Environment `json:"environment"`

	// State is the lifecycle phase, such as "running".
	// Example: fmt.Println(app.Info().State)
	State string `json:"state"`

	// LogLevel is the current minimum log level, such as "INFO".
	// Example: app.SetLogLevel(slog.LevelWarn); fmt.Println(app.Info().LogLevel)
	LogLevel string `json:"log_level"`

	// StartedAt is when the application reached the running state.
	// Example: fmt.Println(app.Info().StartedAt.IsZero()) // true before startup
	StartedAt time.Time `json:"started_at,omitzero"`

	// Uptime is the duration since StartedAt truncated to whole seconds in Go
	// duration string format; empty before startup completes.
	// Example: fmt.Println(app.Info().Uptime)
	Uptime string `json:"uptime,omitzero"`

	// UptimeSeconds is the whole seconds since StartedAt; nil before.
	// Example: if seconds := app.Info().UptimeSeconds; seconds != nil { fmt.Println(*seconds) }
	UptimeSeconds *int64 `json:"uptime_seconds,omitzero"`

	// Build describes the running binary.
	// Example: fmt.Println(app.Info().Build.GoVersion)
	Build BuildInfo `json:"build"`
}

// BuildInfo describes the running binary as recorded by the go command. Every
// field is empty when the binary carries no build information.
// Values come from the binary, not application configuration, and are safe to
// retain as ordinary strings and a boolean. Expose them only to operators.
//
//	build := app.Info().Build
//	fmt.Println(build.GoVersion, build.ModulePath)
type BuildInfo struct {
	// GoVersion is the Go toolchain that built the binary.
	// Example: fmt.Println(app.Info().Build.GoVersion)
	GoVersion string `json:"go_version,omitzero"`

	// ModulePath is the main module path.
	// Example: fmt.Println(app.Info().Build.ModulePath)
	ModulePath string `json:"module_path,omitzero"`

	// ModuleVersion is the main module version, "(devel)" when unknown.
	// Example: fmt.Println(app.Info().Build.ModuleVersion)
	ModuleVersion string `json:"module_version,omitzero"`

	// VCSRevision is the commit the binary was built from.
	// Example: fmt.Println(app.Info().Build.VCSRevision)
	VCSRevision string `json:"vcs_revision,omitzero"`

	// VCSTime is the commit time in RFC 3339 format.
	// Example: fmt.Println(app.Info().Build.VCSTime)
	VCSTime string `json:"vcs_time,omitzero"`

	// VCSModified reports whether the working tree had uncommitted changes.
	// Example: fmt.Println(app.Info().Build.VCSModified)
	VCSModified bool `json:"vcs_modified,omitzero"`
}

// newBuildInfo extracts the reported fields from the result of
// [debug.ReadBuildInfo]. It is empty when ok is false.
func newBuildInfo(build *debug.BuildInfo, ok bool) BuildInfo {
	if !ok || build == nil {
		return BuildInfo{}
	}

	result := BuildInfo{
		GoVersion:     build.GoVersion,
		ModulePath:    build.Main.Path,
		ModuleVersion: build.Main.Version,
	}

	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			result.VCSRevision = setting.Value
		case "vcs.time":
			result.VCSTime = setting.Value
		case "vcs.modified":
			result.VCSModified = setting.Value == "true"
		}
	}

	return result
}

// mainVersion returns the main module version from the result of
// [debug.ReadBuildInfo], or "" when it is unknown or "(devel)".
func mainVersion(build *debug.BuildInfo, ok bool) string {
	if !ok || build == nil || build.Main.Version == develVersion {
		return ""
	}

	return build.Main.Version
}

// Info returns the description [Application.InfoHandler] serves: name,
// version, environment, lifecycle state, log level, start time, uptime, and
// build. It is a snapshot taken now and safe to call in any phase;
// StartedAt, Uptime, and UptimeSeconds are zero, empty, and nil until the
// application has reached [StateRunning]. It reveals versions and revisions,
// so expose it only to operators.
//
// Example:
//
//	info := app.Info()
//	fmt.Println(info.Name, info.State, info.Build.GoVersion)
func (application *Application) Info() Info {
	state, startedAt := application.execution.Snapshot()

	result := Info{
		Name:        application.name,
		Version:     application.version,
		Environment: application.environment,
		State:       state,
		LogLevel:    application.level.Level().String(),
		Build:       application.build,
	}

	if !startedAt.IsZero() {
		uptime := time.Since(startedAt)
		seconds := int64(uptime / time.Second)
		result.StartedAt = startedAt.UTC()
		result.Uptime = uptime.Truncate(time.Second).String()
		result.UptimeSeconds = &seconds
	}

	return result
}
