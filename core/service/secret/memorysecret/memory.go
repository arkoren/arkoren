package memorysecret

import (
	"context"
	"strconv"
	"sync"

	"arkoren.dev/core/service/secret"
)

// Driver stores secret versions in process memory. It is safe for concurrent
// use and requires no Start or Stop calls. Contents are not durable and are
// ordinary process memory, not a protected credential vault; use fixture
// credentials for local development and tests. Construct it with New; the zero
// Driver has no initialized map for writes.
// Versions start at "1" for each new logical secret. Delete removes its history;
// a later Put under the same name starts again at "1", so saved name/version
// selectors can select new contents. Other drivers need not allow immediate
// name reuse or reuse version strings.
//
// Example:
//
//	driver := memorysecret.New()
//	secrets := secret.New(driver)
//	if _, err := secrets.Put(ctx, "api-token", []byte("fixture")); err != nil {
//		return err
//	}
type Driver struct {
	// mutex guards the history map. Stored payloads are immutable, so Get may
	// copy a selected payload after releasing its read lock.
	mutex sync.RWMutex

	// values stores nonempty ordered histories. Writes append owned payloads;
	// Delete removes the entire history without changing returned byte copies.
	values map[string][]secret.Value
}

// settings holds fixture payloads captured by Builder.Values. New materializes
// each entry in private storage without retaining the seed map or byte slices.
type settings struct {
	// Values seeds version 1 of each named secret. The driver copies the map
	// and every byte slice. Nil or empty starts with no secrets.
	Values map[string][]byte
}

// New creates a driver seeded with copies of [Builder.Values] from construction policies.
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// New copies the completed seed map and bytes; callbacks are not retained.
//
// Example:
//
//	memorysecret.New(func(secrets *memorysecret.Builder) {
//		secrets.Values(nil)
//	})
func New(configure ...BuilderFunc) *Driver {
	config := newSettings(configure...)

	driver := &Driver{values: make(map[string][]secret.Value, len(config.Values))}

	for name, value := range config.Values {
		driver.values[name] = []secret.Value{{
			Data:    append([]byte(nil), value...),
			Version: "1",
		}}
	}

	return driver
}

// Get returns a copy of the requested secret version or the current version.
// An empty Version selects current. Missing names or versions return
// secret.ErrNotFound; an already-canceled context returns its error before
// reading. Mutating the returned Data never changes stored bytes.
// A concrete version selects from the current history for that name; after
// deletion and recreation, an old selector can return the new secret's contents.
//
// Example:
//
//	value, err := driver.Get(ctx, secret.Reference{Name: "api-token"})
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
//	clear(value.Data)
func (driver *Driver) Get(ctx context.Context, reference secret.Reference) (secret.Value, error) {
	if err := ctx.Err(); err != nil {
		return secret.Value{}, err
	}

	driver.mutex.RLock()

	versions, found := driver.values[reference.Name]
	value, found := selectedValue(versions, found, reference.Version)
	driver.mutex.RUnlock()

	if !found {
		return secret.Value{}, secret.ErrNotFound
	}

	value.Data = append([]byte(nil), value.Data...)

	return value, nil
}

// selectedValue selects current when version is empty, otherwise an exact
// version match. A found entry always has a nonempty history. The result still
// refers to stored bytes: Get must copy them before returning to its caller.
func selectedValue(versions []secret.Value, found bool, version string) (secret.Value, bool) {
	if !found {
		return secret.Value{}, false
	}

	if version == "" {
		return versions[len(versions)-1], true
	}

	return namedValue(versions, version)
}

// namedValue finds an exact opaque version in an immutable stored history.
// Missing versions return false and a zero Value; matches retain stored bytes
// for Get to copy at the driver boundary.
func namedValue(versions []secret.Value, version string) (secret.Value, bool) {
	for _, candidate := range versions {
		if candidate.Version == version {
			return candidate, true
		}
	}

	return secret.Value{}, false
}

// Put creates name or appends a new current version to an existing secret.
// It copies data before storage and returns a separate caller-owned copy.
// An already-canceled context returns its error without writing.
// An absent name starts at version "1", including after Delete. Saving a version
// for pinned reads does not retain that logical secret across deletion.
//
// Example:
//
//	written, err := driver.Put(ctx, "api-token", []byte("fixture"))
//	if err != nil {
//		return err
//	}
//	rememberVersion(written.Version)
//	clear(written.Data)
func (driver *Driver) Put(ctx context.Context, name string, data []byte) (secret.Value, error) {
	if err := ctx.Err(); err != nil {
		return secret.Value{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	return driver.appendVersion(name, data), nil
}

// Update appends a new current version to an existing secret.
// A missing name returns secret.ErrNotFound and creates nothing. Input and
// returned bytes follow Put's copy rules. An already-canceled context returns
// its error without writing.
//
// Example:
//
//	written, err := driver.Update(ctx, "api-token", []byte("replacement-fixture"))
//	if err != nil {
//		return err
//	}
//	rememberVersion(written.Version)
func (driver *Driver) Update(ctx context.Context, name string, data []byte) (secret.Value, error) {
	if err := ctx.Err(); err != nil {
		return secret.Value{}, err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	if _, found := driver.values[name]; !found {
		return secret.Value{}, secret.ErrNotFound
	}

	return driver.appendVersion(name, data), nil
}

// appendVersion stores a copy of data as the next version of name and returns
// a separate copy, so callers cannot mutate the stored secret. The mutex must
// be held.
func (driver *Driver) appendVersion(name string, data []byte) secret.Value {
	value := secret.Value{
		Data:    append([]byte(nil), data...),
		Version: strconv.Itoa(len(driver.values[name]) + 1),
	}
	driver.values[name] = append(driver.values[name], value)

	value.Data = append([]byte(nil), value.Data...)

	return value
}

// Delete removes name and all of its versions.
// A missing name returns secret.ErrNotFound. An already-canceled context
// returns its error without deleting; previously returned byte copies remain
// owned by their callers and are not cleared by deletion.
// There is no retention or recovery. A later Put creates a new logical secret
// starting at version "1"; an old selector can then return different contents.
//
// Example:
//
//	if err := driver.Delete(ctx, "retired-api-token"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	if _, found := driver.values[name]; !found {
		return secret.ErrNotFound
	}

	delete(driver.values, name)

	return nil
}

var _ secret.Driver = (*Driver)(nil)
