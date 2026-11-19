package federation

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
)

// maxClaimsBytes caps the JSON stored in one connection's claims column. An
// update that would exceed it is skipped with a warning, so a provider that
// sends large profiles can never grow a row without bound.
const maxClaimsBytes = 8 << 10

// protocolClaims are the claims of the OpenID Connect protocol itself. They
// describe the token, not the person, and several of them (iss, sub, aud) are
// the connection's identity: keeping them would invite code that reads a
// stored claim where it should read the verified one, so naming one in
// [ResolverBuilder.KeepClaims] is [ErrInvalidConfig].
var protocolClaims = []string{"iss", "sub", "aud", "exp", "iat", "nbf", "nonce", "at_hash", "auth_time", "scope", "azp", "acr", "amr"}

// validateKeep rejects an allowlist that names an empty claim or a protocol
// claim. The returned error does not wrap [ErrInvalidConfig]; the caller adds it.
func validateKeep(names []string) error {
	for _, name := range names {
		if name == "" {
			return errors.New("claim names must not be empty")
		}
		if slices.Contains(protocolClaims, name) {
			return fmt.Errorf("claim %q is a protocol claim and cannot be kept", name)
		}
	}

	return nil
}

// storable reports whether value is something a connection may keep: a JSON
// scalar (string, boolean or number) or a list of strings. Objects, nested
// lists and null are not, because a stored claim is a profile hint to prefill a
// form with, and structured values invite the application to treat it as data.
func storable(value any) bool {
	switch typed := value.(type) {
	case string, bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case []string:
		return true
	case []any:
		return !slices.ContainsFunc(typed, func(element any) bool {
			_, isString := element.(string)

			return !isString
		})
	default:
		return false
	}
}

// keepClaims returns the claims of an external identity that names allows and
// that are storable. A named claim whose value is not storable is dropped with
// one warning that names the claim and nothing of its value. It returns nil
// when nothing is kept, which is always the case without an allowlist.
func keepClaims(ctx context.Context, claims map[string]any, names []string) map[string]any {
	var kept map[string]any
	for _, name := range names {
		value, present := claims[name]
		if !present {
			continue
		}
		if !storable(value) {
			slog.WarnContext(ctx, "access: claim not kept", "claim", name, "reason", "only scalars and lists of strings can be kept")

			continue
		}
		if kept == nil {
			kept = map[string]any{}
		}
		kept[name] = value
	}

	return kept
}

// decodeClaims decodes the stored claims of a connection. A NULL column and a
// document that is no longer a JSON object (an edit by hand) both give nil, so
// the next login rewrites the column from what the provider sends.
func decodeClaims(stored sql.NullString) map[string]any {
	if !stored.Valid {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal([]byte(stored.String), &claims); err != nil || len(claims) == 0 {
		return nil
	}

	return claims
}

// mergeClaims merges kept over the stored claims and returns the JSON to store
// and whether it differs from stored. A login overwrites the claims it carries
// and keeps the rest: Apple sends the name only on the first login, so a later
// login without it must not lose it. A claim the provider stops sending also
// stays. When the merge would exceed [maxClaimsBytes] it returns false with a
// warning and the stored value stays as it is.
func mergeClaims(ctx context.Context, stored sql.NullString, kept map[string]any) (sql.NullString, bool) {
	if len(kept) == 0 {
		return stored, false
	}
	existing := decodeClaims(stored)
	merged := maps.Clone(existing)
	if merged == nil {
		merged = map[string]any{}
	}
	maps.Copy(merged, kept)
	if reflect.DeepEqual(merged, existing) {
		return stored, false
	}
	encoded, err := json.Marshal(merged)
	if err != nil || len(encoded) > maxClaimsBytes {
		slog.WarnContext(ctx, "access: claims not stored", "reason", "the merged claims do not fit in 8 KiB")

		return stored, false
	}

	return sql.NullString{String: string(encoded), Valid: true}, true
}
