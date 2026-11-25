package secret

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
)

// ErrNotFound identifies a missing logical secret or requested version. Drivers
// wrap it so callers can distinguish absence from permission or provider errors
// with errors.Is; returned errors can still contain sensitive provider details.
//
// Example:
//
//	_, err := secrets.Raw(ctx, "api-token")
//	if errors.Is(err, secret.ErrNotFound) {
//		return fmt.Errorf("provision the required credential: %w", err)
//	}
//	if err != nil {
//		return err
//	}
var ErrNotFound = errors.New("secret not found")

// ErrUnsupported identifies an operation the backend cannot perform, such as a
// write to a read-only source (files, environment variables) or a payload the
// backend cannot store (binary data in a text-only backend). Drivers wrap it so
// callers can tell a missing capability from a missing secret with errors.Is.
// An unsupported operation changes nothing.
//
// Example:
//
//	_, err := secrets.Put(ctx, "env:API_TOKEN", []byte(token))
//	if errors.Is(err, secret.ErrUnsupported) {
//		return fmt.Errorf("environment variables are read-only: %w", err)
//	}
var ErrUnsupported = errors.New("secret operation unsupported")

// ErrUnbound identifies a binding definition (see [NewBinding]) whose key the
// store cannot resolve: no [Builder.Bindings] lookup was configured, the key is
// unset, or its value is empty. Bindings have no defaults, so an unbound
// definition fails before any provider I/O. The error names the binding key,
// never a reference or a secret value.
//
// Example:
//
//	password := secret.NewBinding[string]("DATABASE_PASSWORD")
//	_, err := secrets.Get(ctx, password)
//	if errors.Is(err, secret.ErrUnbound) {
//		return fmt.Errorf("set DATABASE_PASSWORD to a secret reference: %w", err)
//	}
var ErrUnbound = errors.New("secret binding unbound")

// Reference selects a logical secret by name and optionally a version.
// Drivers receive the name exactly as the caller gave it to the [Store]; a
// secretmux driver first removes the "scheme:" prefix it routed on. Concrete versions are immutable within one existing logical secret, not
// permanent identities for a reusable name; see Driver's lifetime rules.
// Names follow provider syntax and versions are provider-issued selectors,
// not redacted diagnostic values. Use a concrete version returned by a write
// rather than deriving one yourself or using a movable provider alias.
//
// Pinning requires keeping the logical secret and concrete version retained
// and readable for the duration of the work. Saving a Reference does not retain
// either, and does not guarantee the old contents after permanent deletion and
// recreation under the same name.
//
// Example:
//
//	reference := secret.Reference{Name: "api-token", Version: written.Version}
//	value, err := secrets.Driver().Get(ctx, reference)
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
type Reference struct {
	// Name identifies the logical secret using the provider's naming syntax.
	// No escaping or normalization is performed by the facade.
	//
	// Example:
	//
	//	reference := secret.Reference{Name: "api-token"}
	//	value, err := secrets.Driver().Get(ctx, reference)
	//	if err != nil {
	//		return err
	//	}
	//	useCredential(value.Data)
	Name string

	// Version selects a version; empty selects the current value on each read.
	// Use a concrete version returned by a write to pin reads within one existing
	// logical secret. Provider-supported aliases can move and do not pin contents.
	// After permanent deletion and recreation, the same name/version selector
	// need not identify the old contents. Treat versions as opaque: do not parse
	// or sort them. See Driver for lifetime and provider-retention rules.
	//
	// Example:
	//
	//	reference := secret.Reference{Name: "api-token", Version: written.Version}
	//	value, err := secrets.Driver().Get(ctx, reference)
	//	if err != nil {
	//		return err
	//	}
	//	useCredential(value.Data)
	Version string
}

// Value contains a secret payload and its provider-issued opaque version.
// Driver operations return caller-owned bytes, but copying this record alone
// shares its Data slice. Neither the record nor its contents are redacted when
// formatted; never log it as an ordinary diagnostic value.
//
// Example:
//
//	written, err := secrets.Put(ctx, "api-token", []byte(token))
//	if err != nil {
//		return err
//	}
//	rememberVersion(written.Version)
//	clear(written.Data) // Clears this caller's copy, not the stored payload.
type Value struct {
	// Data contains the caller-owned payload returned by a driver. Mutating or
	// clearing it does not change storage or another read's returned value.
	//
	// Example:
	//
	//	value, err := secrets.Driver().Get(ctx, secret.Reference{Name: "api-token"})
	//	if err != nil {
	//		return err
	//	}
	//	useCredential(value.Data)
	//	clear(value.Data)
	Data []byte

	// Version identifies the immutable payload version within one existing
	// logical secret, independently of the returned byte slice. Pass it back
	// unchanged when selecting that version. It is not a permanent identity
	// across deletion and recreation under the same name; see Reference.
	//
	// Example:
	//
	//	reference := secret.Reference{Name: "api-token", Version: written.Version}
	//	value, err := secrets.Driver().Get(ctx, reference)
	//	if err != nil {
	//		return err
	//	}
	//	useCredential(value.Data)
	Version string
}

// Driver retrieves and mutates versioned logical secrets. Every write creates
// a new immutable concrete version whose provider-issued Version differs from
// all earlier versions within that existing logical secret. Later writes to
// that secret do not change earlier version payloads. Data passed to and
// returned from a driver is copied, so callers may mutate it. Every operation
// fails with an error wrapping the context's error when ctx is already canceled,
// and then changes nothing.
//
// Backends may restrict what a payload can contain, for example to UTF-8 text
// or to a JSON object; drivers document such limits. Raw driver calls bypass
// Store telemetry and notifications.
//
// Permanent deletion followed by recreation under the same name creates a new
// logical secret. A name/version selector may then be reused and select different
// contents; reuse is permitted, not required. Recovery of a retained object is
// not recreation. Providers may delay or prevent name availability and may
// provide stronger version or resource identities. Pinning does not retain a
// secret or version or guarantee continued availability; see Reference.
// These immutability guarantees cover concrete payloads, not provider metadata,
// access policies, or movable aliases.
//
// Example:
//
//	var driver secret.Driver = secrets.Driver()
//	value, err := driver.Get(ctx, secret.Reference{Name: "api-token"})
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
type Driver interface {
	// Get retrieves reference. An empty Reference.Version selects the current
	// value. A missing secret, or a version of it that was never issued, returns
	// an error wrapping ErrNotFound. Concrete versions select from the existing
	// logical secret's history, not a name reserved forever: an old selector
	// need not identify old contents after permanent deletion and recreation.
	// Returned bytes belong to the caller.
	//
	// Example:
	//
	//	value, err := driver.Get(ctx, secret.Reference{Name: "api-token"})
	//	if err != nil {
	//		return err
	//	}
	//	useCredential(value.Data)
	Get(ctx context.Context, reference Reference) (Value, error)

	// Put creates name when absent or writes a new current value version when
	// present. It returns the written value with its new version. Both input
	// and returned bytes are detached from the stored payload. Version identity
	// follows Driver's logical-secret lifetime; saving a version does not retain
	// the logical secret or its history.
	//
	// Example:
	//
	//	written, err := driver.Put(ctx, "api-token", []byte(token))
	//	if err != nil {
	//		return err
	//	}
	//	rememberVersion(written.Version)
	Put(ctx context.Context, name string, value []byte) (Value, error)

	// Update writes a new current value version for an existing logical secret
	// and returns it. It returns an error wrapping ErrNotFound, and creates
	// nothing, when name does not exist. It follows Put's byte ownership rules.
	//
	// Example:
	//
	//	written, err := driver.Update(ctx, "api-token", []byte(replacement))
	//	if err != nil {
	//		return err
	//	}
	//	rememberVersion(written.Version)
	Update(ctx context.Context, name string, value []byte) (Value, error)

	// Delete makes name and all of its versions unavailable through Get, which
	// then returns an error wrapping ErrNotFound. Deleting a name that does not
	// exist returns an error wrapping ErrNotFound. Subsequent provider-side
	// recovery or recreation can make the name available again; recreation
	// does not guarantee old selector identities. Provider retention can delay
	// or prevent name reuse. Deletion is not a rotation operation; use Put or
	// Update to add a version without deleting the existing logical secret.
	//
	// Example:
	//
	//	if err := driver.Delete(ctx, "retired-api-token"); err != nil {
	//		return err
	//	}
	Delete(ctx context.Context, name string) error
}

// Store provides typed retrieval and observed mutations over a driver.
// Secrets are named by references, strings of the form "scheme:name" such as
// "aws:billing/key", "file:/run/secrets/token" or "env:API_TOKEN". The Store
// does not parse references: it hands them to its driver unchanged, and a
// secretmux driver routes on the scheme. It does not cache secrets or start
// observability services. Driver access bypasses facade telemetry and mutation
// notifications. Construct it with New; the zero Store has no driver and is not
// ready for operations.
//
// Example:
//
//	secrets := secret.New(driver)
//	token, err := secrets.Get(ctx, secret.NewSecret[string]("aws:billing/api-token"))
//	if err != nil {
//		return err
//	}
//	useCredential(token)
type Store struct {
	// driver is the underlying contract implementation used for delegation.
	driver Driver

	// config is the detached construction snapshot of shared services and the
	// bindings lookup.
	config settings

	// instruments reuses instruments for the currently resolved telemetry service.
	instruments *telemetry.Instruments[clientop.Instruments]
}

// New creates a Store that delegates to driver. Callbacks run directly once
// in order; nil entries panic. Nil telemetry follows telemetry.Default per
// operation; nil events disables notifications. Construction performs no I/O.
// Expanded nil or empty callback slices omit edits. Publication copies the
// settings, not the driver or service objects. Callbacks are not retained.
// New does not validate or start driver; operations that reach a nil Driver
// interface panic. The caller retains any driver-specific lifecycle ownership.
//
// Example:
//
//	secrets := secret.New(driver, func(secrets *secret.Builder) {
//		secrets.Events(bus)
//	})
func New(driver Driver, configure ...BuilderFunc) *Store {
	secrets := &Builder{}

	for _, configure := range configure {
		configure(secrets)
	}

	return &Store{
		driver:      driver,
		config:      secrets.settings,
		instruments: telemetry.NewInstruments(clientop.Build(operationNames)),
	}
}

// Driver returns the shared underlying Driver, not a copy. Calls made through
// it bypass this Store's telemetry and mutation notifications; use it when raw
// version selection is necessary and own the resulting observation policy.
// For pinned work, keep the logical secret and concrete version retained and
// readable until the work completes. A saved Reference alone does not guarantee
// the old contents after permanent deletion and recreation under the same name.
//
// Example:
//
//	value, err := secrets.Driver().Get(ctx, secret.Reference{
//		Name:    "api-token",
//		Version: written.Version,
//	})
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
func (store *Store) Driver() Driver {
	return store.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() Driver method, so wrappers such as prefixing or tracing
// drivers do not hide the concrete driver. Failure returns the zero T and
// false. The returned driver stays shared and bypasses facade observation.
//
// With memorysecret imported from arkoren.dev/core/service/secret/memorysecret:
//
// Example:
//
//	driver, ok := secrets.DriverAs[*memorysecret.Driver]()
//	if !ok {
//		return errors.New("store does not use the memory driver")
//	}
//	value, err := driver.Get(ctx, secret.Reference{Name: "api-token"})
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
func (store *Store) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](store.Driver())
}

// Raw retrieves the current unmodified bytes of reference, owned by the caller.
// It applies no decoding, so trailing newlines and non-UTF-8 bytes survive.
// One span and metric observation cover the provider read, including failures;
// no reference, version, bytes, size, or native error text is recorded.
//
// Example:
//
//	data, err := secrets.Raw(ctx, "file:/run/secrets/token")
//	if err != nil {
//		return err
//	}
//	useCredential(data)
func (store *Store) Raw(ctx context.Context, reference string) ([]byte, error) {
	ctx, op := store.begin(ctx, "get")

	value, err := store.driver.Get(ctx, Reference{Name: reference})
	end(ctx, op, err)

	if err != nil {
		return nil, err
	}

	return value.Data, nil
}

// Put creates name or writes a new current version and returns the copied
// payload with its opaque version. Driver errors are returned unchanged.
// Version identity follows Driver's logical-secret lifetime; saving a version
// does not retain the logical secret or that version for future pinned reads.
// Telemetry and best-effort mutation notifications describe the observed API
// outcome, not provider-side rotation or application rollout.
//
// Example:
//
//	written, err := secrets.Put(ctx, "api-token", []byte(token))
//	if err != nil {
//		return err
//	}
//	rememberVersion(written.Version)
func (store *Store) Put(ctx context.Context, reference string, value []byte) (Value, error) {
	ctx, op := store.begin(ctx, "put")

	written, err := store.driver.Put(ctx, reference, value)
	end(ctx, op, err)
	store.mutated(ctx, "put", err)

	return written, err
}

// Update writes a new current version of an existing secret. Missing names
// return ErrNotFound without creating a secret; driver errors pass unchanged.
// Like Put, notifications report only the observed API outcome.
//
// Example:
//
//	_, err := secrets.Update(ctx, "api-token", []byte(token))
//	if err != nil {
//		return err
//	}
func (store *Store) Update(ctx context.Context, reference string, value []byte) (Value, error) {
	ctx, op := store.begin(ctx, "update")

	written, err := store.driver.Update(ctx, reference, value)
	end(ctx, op, err)
	store.mutated(ctx, "update", err)

	return written, err
}

// Delete makes name and all versions unavailable through the driver. Missing
// names return ErrNotFound. Provider retention can delay or prevent name reuse;
// a success notification does not imply permanent erasure or backup removal.
// Recovery of a retained object is not recreation. Permanent deletion followed
// by recreation creates a new logical secret, so saved name/version selectors
// need not identify the old contents; see Driver. Errors pass unchanged. Use
// Put or Update to rotate by adding a version, not by deleting the secret.
//
// Example:
//
//	if err := secrets.Delete(ctx, "retired-api-token"); err != nil {
//		return err
//	}
func (store *Store) Delete(ctx context.Context, reference string) error {
	ctx, op := store.begin(ctx, "delete")

	err := store.driver.Delete(ctx, reference)

	end(ctx, op, err)
	store.mutated(ctx, "delete", err)

	return err
}

// Get resolves definition to a reference, reads its current bytes and decodes
// them into T by the rules of [Secret]: a string loses one trailing newline and
// must be valid UTF-8, []byte is copied, an [encoding.TextUnmarshaler] receives
// the text without that newline, and anything else is decoded as JSON. A
// binding definition looks its key up through [Builder.Bindings] first and
// fails with [ErrUnbound] when it cannot. An already-canceled context takes
// precedence over an invalid definition; both are rejected before provider I/O.
//
// Get shares [Store.Raw]'s single provider observation: a decode error returned
// afterward does not turn a successful provider read into a telemetry failure.
// Decode errors retain the reference and cause; handle them without logging
// sensitive details. The raw bytes are cleared once decoded. Definitions do not
// enforce a schema.
//
// Example:
//
//	database := secret.NewSecret[map[string]string]("vault:app/database")
//	config, err := secrets.Get(ctx, database)
//	if err != nil {
//		return err
//	}
//	useCredential(config["password"])
func (store *Store) Get[T any](ctx context.Context, definition Secret[T]) (res T, err error) {
	if err := ctx.Err(); err != nil {
		return res, err
	}

	if err := definition.Err(); err != nil {
		return res, err
	}

	reference, err := definition.resolve(store.config.bindings)

	if err != nil {
		return res, err
	}

	data, err := store.Raw(ctx, reference)

	if err != nil {
		return res, err
	}

	defer clear(data)

	res, err = definition.decode(data)

	if err != nil {
		return res, fmt.Errorf("decode secret %q: %w", reference, err)
	}

	return res, nil
}
