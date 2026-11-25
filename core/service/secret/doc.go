// Package secret provides versioned secret contracts, typed retrieval, and
// observed mutations. Wrap a Driver with New; Get reads and decodes the current
// value of a definition, Raw reads its bytes, and Put, Update, and Delete
// delegate mutation semantics.
//
// # References
//
// A secret is named by a reference, a string "scheme:name" such as
// "aws:billing/key", "vault:app/db", "file:/run/secrets/token" or
// "env:API_TOKEN". There is no "?field" or "?version" syntax. The Store never
// parses a reference: it passes it to the driver, and the secretmux package
// splits it at the first colon to pick the driver registered for the scheme.
// Raw driver calls take a [Reference], whose Name is that string (without the
// scheme once secretmux has routed it) and whose Version is a provider-issued
// selector.
//
// # Definitions
//
// Get takes an immutable [Secret] definition binding a reference to the Go type
// its payload decodes into. [NewSecret] declares a static reference;
// [NewBinding] declares a key that the store resolves at each Get through the
// lookup given to [Builder.Bindings] (usually os.LookupEnv), so a deployment
// chooses the backend by setting DATABASE_PASSWORD=aws:prod/db. A binding must
// be bound: an unset key or empty value fails with [ErrUnbound], and there are
// no defaults. Optional immutable [Namespace] definitions compose related
// literal references with a required provider-appropriate separator, including
// empty for concatenation; their generic Secret method picks each leaf's type.
// Subnamespace joins with the parent separator before child policies change
// future joins; Derive keeps settings without reapplying defaults. Invalid
// ancestors remain invalid through children and leaves. Namespaces have no
// provider isolation, schema registration, or bulk-operation semantics.
// Definitions neither cache values nor enforce schemas across deployments.
//
// Example:
//
//	secrets := secret.New(driver, func(secrets *secret.Builder) {
//		secrets.Bindings(os.LookupEnv)
//	})
//	credentials := secret.NewNamespace("vault:production", "/")
//	database := credentials.Subnamespace("database").Secret[map[string]string]("credentials")
//	config, err := secrets.Get(ctx, database)
//	if err != nil {
//		return err
//	}
//	useCredential(config["password"])
//
// # Decoding
//
// Drivers carry bytes only; decoding belongs to the definition's type. A
// string must be valid UTF-8 and loses one trailing "\n" or "\r\n"; a []byte is
// a copy; a type whose pointer implements [encoding.TextUnmarshaler] (such as
// key.Shared or key.Ring) receives the text without that one newline; anything
// else is decoded as JSON with encoding/json/v2. A named string type is not a
// string here and decodes as a JSON string. Decode errors never contain the
// payload.
//
// # Backends
//
// Backends differ in what they store. A driver that cannot perform an
// operation, such as a write to a read-only source (files, environment
// variables) or a binary payload to a text-only backend, returns an error
// wrapping [ErrUnsupported] and changes nothing. A missing secret is
// [ErrNotFound]. The secrettest suite expresses these capabilities as options.
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
// arkoren.secret.operation.duration (seconds). Raw and typed Get share a single
// provider observation. Decode failures and unbound bindings occur outside it
// and do not turn a successful provider read into a telemetry failure.
// Nil Builder.Telemetry follows telemetry.Default per call. Disabled telemetry
// records nothing. Attributes contain only operation and bounded error class:
// references, versions, payloads, sizes, and native error types/text are omitted.
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
//	token, err := secrets.Get(ctx, secret.NewSecret[string]("aws:api-token"))
//	if err != nil {
//		return err
//	}
//	useCredential(token)
package secret
