package key

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
)

// ErrInvalid reports malformed, unsupported, missing, or zero key material.
// Errors never contain key contents or parser diagnostics derived from them.
//
// Example:
//
//	if _, err := key.ParseShared("invalid"); errors.Is(err, key.ErrInvalid) {
//		fmt.Println("invalid configuration")
//	}
var ErrInvalid = errors.New("invalid key material")

// maxFileSize bounds file reads and encoded input, including PEM whitespace.
const maxFileSize = 64 * 1024

// Shared is immutable 32-byte symmetric key material for encryption or signing.
// Use an independently generated secret for each purpose. Links and cursors
// derive their own MAC keys from it. Its zero value is invalid.
//
// Example:
//
//	material, err := key.GenerateShared()
//	if err != nil {
//		return err
//	}
//	_ = material.Bytes()
type Shared struct {
	// material owns the raw secret independently of its input.
	material string
}

// NewShared validates and copies exactly 32 bytes of raw symmetric key material.
// Validation cannot establish entropy. GenerateShared is the provisioning path.
//
// Example:
//
//	rawKeyBytes := make([]byte, 32)
//	rand.Read(rawKeyBytes)
//	material, err := key.NewShared(rawKeyBytes)
//	if err != nil {
//		return err
//	}
//	_ = material
func NewShared(raw []byte) (Shared, error) {
	if len(raw) != 32 {
		return Shared{}, ErrInvalid
	}

	return Shared{material: string(raw)}, nil
}

// GenerateShared provisions 32 independent random bytes using crypto/rand.
// It does not persist the material; callers must store it securely.
//
// Example:
//
//	material, err := key.GenerateShared()
//	if err != nil {
//		return err
//	}
//	_ = material
func GenerateShared() (Shared, error) {
	return NewShared(randomBytes())
}

// ParseShared parses padded standard base64 and requires exactly 32 decoded bytes.
// Surrounding whitespace is accepted; embedded line breaks, noncanonical
// padding bits, and inputs over 64 KiB return ErrInvalid. Input is not retained.
//
// Example:
//
//	provisioned, err := key.GenerateShared()
//	if err != nil {
//		return err
//	}
//	material, err := key.ParseShared(provisioned.Base64())
//	if err != nil {
//		return err
//	}
//	_ = material
func ParseShared(encoded string) (Shared, error) {
	raw, err := decode(encoded)

	if err != nil {
		return Shared{}, err
	}

	return NewShared(raw)
}

// LoadShared reads and parses a base64 symmetric-secret file without changing it.
// Reads are bounded to 64 KiB. OS errors retain their causes; invalid file
// contents return ErrInvalid without exposing material.
//
// Example:
//
//	material, err := key.LoadShared("encryption.key")
//	if err != nil {
//		return err
//	}
//	_ = material
func LoadShared(path string) (Shared, error) {
	raw, err := readFile(path)

	if err != nil {
		return Shared{}, err
	}

	return ParseShared(string(raw))
}

// Err reports whether the value contains exactly 32 bytes of key material.
// It returns ErrInvalid for a zero value. Consumers use it to reject missing
// configuration rather than silently generating replacement material.
//
// Example:
//
//	if err := (key.Shared{}).Err(); errors.Is(err, key.ErrInvalid) {
//		fmt.Println("configure a shared key")
//	}
func (secret Shared) Err() error {
	if len(secret.material) != 32 {
		return ErrInvalid
	}

	return nil
}

// Bytes explicitly exports a detached copy of the private symmetric material.
// The zero value exports an empty slice. Mutating output cannot change the key.
//
// Example:
//
//	material, err := key.LoadShared("encryption.key")
//	if err != nil {
//		return err
//	}
//	_ = material.Bytes()
func (secret Shared) Bytes() []byte {
	return []byte(secret.material)
}

// Base64 explicitly exports the symmetric secret in padded standard base64.
// A zero value exports an empty string. Protect this representation just like
// raw private material; ordinary formatting deliberately redacts it.
//
// Example:
//
//	material, err := key.GenerateShared()
//	if err != nil {
//		return err
//	}
//	return os.WriteFile("encryption.key", []byte(material.Base64()), 0o600)
func (secret Shared) Base64() string {
	return base64.StdEncoding.EncodeToString(secret.Bytes())
}

// String redacts secret material in ordinary formatting.
// Zero and valid values use the same marker, not a material-derived identifier.
//
// Example:
//
//	fmt.Println(key.Shared{}) // [REDACTED shared key]
func (secret Shared) String() string {
	return "[REDACTED shared key]"
}

// GoString redacts secret material in Go-syntax formatting.
// It never includes the underlying private bytes, even for a zero value.
//
// Example:
//
//	fmt.Printf("%#v", key.Shared{}) // [REDACTED shared key]
func (secret Shared) GoString() string {
	return secret.String()
}

// LogValue redacts secret material in structured logs.
// Explicit Bytes or Base64 exports are required to obtain the private bytes.
//
// Example:
//
//	slog.Info("configuration", "material", key.Shared{})
func (secret Shared) LogValue() slog.Value {
	return slog.StringValue(secret.String())
}

// randomBytes returns 256 bits from the operating system's secure source.
// crypto/rand.Read terminates the process if secure randomness is unavailable.
func randomBytes() []byte {
	raw := make([]byte, 32)
	rand.Read(raw)

	return raw
}

// decode accepts one bounded canonical base64 value, not wrapped MIME base64.
func decode(encoded string) ([]byte, error) {
	if len(encoded) > maxFileSize {
		return nil, ErrInvalid
	}

	encoded = strings.TrimSpace(encoded)

	if strings.ContainsAny(encoded, "\r\n") {
		return nil, ErrInvalid
	}

	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)

	if err != nil {
		return nil, ErrInvalid
	}

	return raw, nil
}

// readFile bounds reads even for nonregular files and preserves OS error causes.
func readFile(path string) ([]byte, error) {
	file, err := os.Open(path)

	if err != nil {
		return nil, err
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	err = errors.Join(err, file.Close())

	if err != nil {
		return nil, err
	}

	if len(raw) > maxFileSize {
		return nil, ErrInvalid
	}

	return raw, nil
}
