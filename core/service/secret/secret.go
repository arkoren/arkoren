package secret

import (
	"context"
	"encoding/json/v2"
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

// Reference selects a logical secret by name and optionally a version.
// Concrete versions are immutable within one existing logical secret, not
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
// It does not cache secrets or start observability services. Driver access
// bypasses facade telemetry and mutation notifications. Construct it with New;
// the zero Store has no driver and is not ready for operations.
//
// Example:
//
//	secrets := secret.New(driver)
//	value, err := secrets.String(ctx, "api-token")
//	if err != nil {
//		return err
//	}
//	useCredential(value)
type Store struct {
	// driver is the underlying contract implementation used for delegation.
	driver Driver

	// config is the detached construction snapshot of shared services.
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

// Raw retrieves the current unmodified bytes of name, owned by the caller.
// One span and metric observation cover the provider read, including failures;
// no name, version, bytes, size, or native error text is recorded.
//
// Example:
//
//	data, err := secrets.Raw(ctx, "api-token")
//	if err != nil {
//		return err
//	}
//	useCredential(data)
func (store *Store) Raw(ctx context.Context, name string) ([]byte, error) {
	ctx, op := store.begin(ctx, "get")

	value, err := store.driver.Get(ctx, Reference{Name: name})
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
func (store *Store) Put(ctx context.Context, name string, value []byte) (Value, error) {
	ctx, op := store.begin(ctx, "put")

	written, err := store.driver.Put(ctx, name, value)
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
func (store *Store) Update(ctx context.Context, name string, value []byte) (Value, error) {
	ctx, op := store.begin(ctx, "update")

	written, err := store.driver.Update(ctx, name, value)
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
func (store *Store) Delete(ctx context.Context, name string) error {
	ctx, op := store.begin(ctx, "delete")

	err := store.driver.Delete(ctx, name)
	end(ctx, op, err)
	store.mutated(ctx, "delete", err)

	return err
}

// String retrieves the current value of name as a string without UTF-8
// validation. It shares Raw's single provider observation and returns provider
// errors unchanged; string conversion adds no span, metric, or event.
//
// Example:
//
//	token, err := secrets.String(ctx, "api-token")
//	if err != nil {
//		return err
//	}
//	useCredential(token)
func (store *Store) String(ctx context.Context, name string) (string, error) {
	value, err := store.Raw(ctx, name)

	if err != nil {
		return "", err
	}

	return string(value), nil
}

// Get retrieves the current value of definition and JSON-decodes it into T. It shares
// Raw's single provider observation: a JSON decode error returned afterward does
// not turn a successful provider read into a telemetry failure. Decode errors
// retain the name and cause; handle them without logging sensitive details.
// An already-canceled context takes precedence over an invalid definition;
// both are rejected before provider I/O. Definitions do not enforce a schema.
//
// Example:
//
//	database := secret.NewSecret[map[string]string]("database")
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

	value, err := store.Raw(ctx, definition.name)

	if err != nil {
		return res, err
	}

	if err := json.Unmarshal(value, &res); err != nil {
		return res, fmt.Errorf("decode secret %q: %w", definition.name, err)
	}

	return res, nil
}
