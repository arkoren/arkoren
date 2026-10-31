// Package secret provides versioned secret contracts, typed retrieval, and
// observed mutations. Wrap a Driver with New; Raw, String, and Get read the
// current value, while Put, Update, and Delete delegate mutation semantics.
// Get takes an immutable Secret definition binding the name to its decoding type;
// raw version selection remains available through Driver.Get and Reference.
// Definitions neither cache values nor enforce schemas across deployments.
// Optional immutable Namespace definitions compose related literal names with a
// required provider-appropriate separator, including empty for concatenation.
// Subnamespace joins with the parent separator before child policies change
// future joins; Derive keeps settings without reapplying defaults. Invalid
// ancestors remain invalid through children and leaves. Namespaces have no
// provider isolation, schema registration, or bulk-operation semantics.
//
// Example:
//
//	credentials := secret.NewNamespace[map[string]string]("production", "/")
//	database := credentials.Subnamespace("database").Secret("credentials")
//	config, err := secrets.Get(ctx, database)
//	if err != nil {
//		return err
//	}
//	useCredential(config["password"])
//
// # Version lifetime
//
// Concrete versions are immutable and unique within one existing logical
// secret. Put and Update add versions without changing earlier payloads.
// Permanent deletion followed by recreation under the same name creates a new
// logical secret: a saved name/version selector may be reused and select
// different contents. Reuse is permitted, not required; providers may provide
// stronger version or resource identities. Recovery of a retained object is
// not recreation, and provider retention may delay or prevent name availability.
//
// To pin reads through Driver.Get, use a concrete version returned by a write,
// not a movable provider alias. Keep that logical secret and version retained
// and readable for the duration of the work; saving a Reference retains neither.
// Rotation adds a version with Put or Update, rather than deleting the secret.
//
// # Observation
//
// Each provider operation emits a "secret get", "secret put", "secret update",
// or "secret delete" span plus arkoren.secret.operations and
// arkoren.secret.operation.duration (seconds). String and typed Get share Raw's
// single provider observation. JSON decode failures occur after that observation
// and do not turn a successful provider read into a telemetry failure.
// Nil Builder.Telemetry follows telemetry.Default per call. Disabled telemetry
// records nothing. Attributes contain only operation and bounded error class:
// names, versions, payloads, sizes, and native error types/text are omitted.
//
// Builder.Events opts into best-effort MutationSucceeded and MutationFailed
// notifications for facade writes only; nil disables them. These are observed
// API outcomes, not a global rotation feed, authoritative audit, rollout signal,
// or durability guarantee. Driver and native client access bypass observation.
// Callers must still handle returned errors, which retain their original identity
// and potentially sensitive text. Never log secret values or errors blindly.
//
// Example:
//
//	secrets := secret.New(driver, func(secrets *secret.Builder) {
//		secrets.Events(bus)
//	})
//	token, err := secrets.String(ctx, "api-token")
//	if err != nil {
//		return err
//	}
//	useCredential(token)
package secret
