// Package signature owns the versioned link MAC format, not link policy.
package signature

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"slices"
)

const (
	// canonicalPrefix versions the authenticated link representation.
	canonicalPrefix = "arkoren.dev/core/http/link/v1\n"
	// keyInfo binds derived keys to the link signature purpose.
	keyInfo = "arkoren.dev/core/http/link/v1"
)

// Derive creates purpose-bound MAC material without retaining the input key.
func Derive(secret []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, nil, keyInfo, sha256.Size)
}

// Canonical preserves escaped paths, sorted queries, and optional bindings.
// Absolute mode covers the normalized origin; relative mode covers only the
// target path and query. Binding presence is distinct from empty binding bytes.
func Canonical(target *url.URL, query url.Values, absolute bool, ignored []string, binding []byte, bound bool) string {
	signed := url.Values{}

	for name, values := range query {
		if name != "signature" && !slices.Contains(ignored, name) {
			signed[name] = values
		}
	}

	canonical := canonicalPrefix + "relative\n" + target.EscapedPath()

	if absolute {
		canonical = canonicalPrefix + "absolute\n" + target.Scheme + "://" + target.Host + target.EscapedPath()
	}

	if encoded := signed.Encode(); encoded != "" {
		canonical += "?" + encoded
	}

	if bound {
		digest := sha256.Sum256(binding)
		canonical += "\nbound\n" + base64.RawURLEncoding.EncodeToString(digest[:])
	}

	return canonical
}

// MAC authenticates canonical bytes without retaining or changing either input.
func MAC(material []byte, canonical string) []byte {
	mac := hmac.New(sha256.New, material)
	// hash.Hash.Write always accepts the complete input without an error.
	_, _ = mac.Write([]byte(canonical))

	return mac.Sum(nil)
}
