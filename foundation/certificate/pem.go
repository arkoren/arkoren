package certificate

import (
	"bytes"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"io"
	"os"
)

// maxInputSize bounds each PEM input and each file read to one MiB, including
// whitespace. It permits ordinary chains without unbounded parser allocation.
const maxInputSize = 1024 * 1024

// decodeBlock consumes the first PEM envelope, allowing surrounding whitespace
// but no skipped text, malformed earlier envelopes, or legacy PEM headers.
func decodeBlock(encoded []byte) (*pem.Block, []byte, error) {
	encoded = bytes.TrimSpace(encoded)
	block, rest := pem.Decode(encoded)

	if block == nil || len(block.Headers) != 0 {
		return nil, nil, ErrInvalid
	}

	prefix := []byte("-----BEGIN " + block.Type + "-----")

	if !bytes.HasPrefix(encoded, prefix) {
		return nil, nil, ErrInvalid
	}

	consumed := encoded[:len(encoded)-len(rest)]

	if bytes.Count(consumed, []byte("-----BEGIN ")) != 1 {
		return nil, nil, ErrInvalid
	}

	return block, bytes.TrimSpace(rest), nil
}

// parsePrivate accepts one unencrypted PKCS #1, SEC 1, or PKCS #8 key. All
// parser diagnostics are deliberately replaced so private data cannot enter
// errors. Canonical PKCS #8 encoding is performed by ParseIdentity afterward.
func parsePrivate(encoded []byte) (any, error) {
	if len(encoded) > maxInputSize {
		return nil, ErrInvalid
	}

	block, rest, err := decodeBlock(encoded)

	if err != nil || len(rest) != 0 {
		return nil, ErrInvalid
	}

	var value asn1.RawValue
	trailing, err := asn1.Unmarshal(block.Bytes, &value)

	if err != nil || len(trailing) != 0 {
		return nil, ErrInvalid
	}

	return parsePrivateDER(block)
}

// parsePrivateDER dispatches only the three supported unencrypted envelopes.
// Its errors stay private to ParseIdentity, which replaces native diagnostics.
func parsePrivateDER(block *pem.Block) (any, error) {
	switch block.Type {
	case "PRIVATE KEY":
		return x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, ErrInvalid
	}
}

// encodePEM allocates a fresh buffer for a validated DER value. Public and
// private callers supply their distinct envelope labels explicitly.
func encodePEM(label string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: label, Bytes: der})
}

// readFile bounds allocation even for nonregular files and preserves open,
// read, and close error causes. It neither writes nor changes permissions.
func readFile(path string) ([]byte, error) {
	file, err := os.Open(path)

	if err != nil {
		return nil, err
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxInputSize+1))
	err = errors.Join(err, file.Close())

	if err != nil {
		return nil, err
	}

	if len(raw) > maxInputSize {
		return nil, ErrInvalid
	}

	return raw, nil
}
