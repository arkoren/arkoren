package key

import (
	"bytes"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
)

// decodePEM requires one bounded PEM envelope with the requested label and
// exactly one DER value. The prefix/count checks prevent pem.Decode from
// skipping malformed leading input. All malformed input maps to ErrInvalid.
func decodePEM(encoded []byte, label string) ([]byte, error) {
	if len(encoded) > maxFileSize {
		return nil, ErrInvalid
	}

	encoded = bytes.TrimSpace(encoded)

	if !bytes.HasPrefix(encoded, []byte("-----BEGIN "+label+"-----")) ||
		bytes.Count(encoded, []byte("-----BEGIN ")) != 1 {
		return nil, ErrInvalid
	}

	block, rest := pem.Decode(encoded)

	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrInvalid
	}

	if block.Type != label || len(block.Headers) != 0 {
		return nil, ErrInvalid
	}

	return completeDER(block.Bytes)
}

// completeDER rejects trailing ASN.1 data because the standard-library
// PKCS #8 parser may otherwise silently ignore bytes after its outer value.
func completeDER(der []byte) ([]byte, error) {
	var value asn1.RawValue
	trailing, err := asn1.Unmarshal(der, &value)

	if err != nil || len(trailing) != 0 {
		return nil, ErrInvalid
	}

	return der, nil
}

// parsePrivate decodes only unencrypted PKCS #8 PEM. Category constructors
// validate the concrete algorithm; parser diagnostics never escape to logs.
func parsePrivate(encoded []byte) (any, error) {
	der, err := decodePEM(encoded, "PRIVATE KEY")

	if err != nil {
		return nil, err
	}

	private, err := x509.ParsePKCS8PrivateKey(der)

	if err != nil {
		return nil, ErrInvalid
	}

	return private, nil
}

// encodePEM serializes validated native material into a fresh PEM buffer.
// Callers choose the standard PKCS #8 or SPKI envelope label.
func encodePEM(label string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: label, Bytes: der})
}
