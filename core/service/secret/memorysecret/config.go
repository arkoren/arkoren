package memorysecret

import "slices"

// BuilderFunc edits the seed settings of one fresh construction draft. New
// invokes callbacks directly once, synchronously in order; nil entries panic,
// while an expanded nil or empty slice supplies no edits. Seed containers are
// copied during capture and again when New constructs the driver.
//
// Example:
//
//	var fixtures memorysecret.BuilderFunc = func(secrets *memorysecret.Builder) {
//		secrets.Values(map[string][]byte{"api-token": []byte("fixture")})
//	}
//	driver := memorysecret.New(fixtures)
//	value, err := driver.Get(ctx, secret.Reference{Name: "api-token"})
//	if err != nil {
//		return err
//	}
//	useCredential(value.Data)
type BuilderFunc = func(*Builder)

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	memorysecret.New(func(secrets *memorysecret.Builder) {
//		secrets.Values(nil)
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies policies directly once to a fresh draft. Values has
// already detached caller inputs; New copies the completed seeds into storage
// so later draft edits cannot change a published driver. No defaults need
// normalization: an omitted Values setting starts with no secrets.
func newSettings(configure ...BuilderFunc) settings {
	secrets := &Builder{}

	for _, configure := range configure {
		configure(secrets)
	}

	return secrets.settings
}

// Values replaces the seed secrets, copying the map and each byte slice before
// retention. Nil or empty input clears all seeds. New seeds version one from
// the completed draft and copies its bytes again; later edits to the input,
// retained draft or returned secret values cannot change the driver's seeds.
//
// Example:
//
//	memorysecret.New(func(secrets *memorysecret.Builder) {
//		secrets.Values(map[string][]byte{"api": []byte("local-value")})
//	})
func (secrets *Builder) Values(values map[string][]byte) *Builder {
	secrets.settings.Values = cloneValues(values)

	return secrets
}

// cloneValues detaches both the seed map and each secret byte slice.
// It preserves nil input and does not retain caller-owned secret storage.
func cloneValues(values map[string][]byte) map[string][]byte {
	if values == nil {
		return nil
	}

	copied := make(map[string][]byte, len(values))

	for name, value := range values {
		copied[name] = slices.Clone(value)
	}

	return copied
}
