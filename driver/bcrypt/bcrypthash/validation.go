package bcrypthash

import (
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// encodedSaltSize is the fixed character count for bcrypt's unpadded 16-byte
// salt. It separates the salt from the digest in a stored record's payload.
const encodedSaltSize = 22

// encodedDigestSize is the fixed character count for bcrypt's unpadded 23-byte
// digest. Extra or missing characters are malformed, not password mismatches.
const encodedDigestSize = 31

// validatedCost reads the native cost only for a complete, canonically encoded
// record. Native Cost alone does not validate salt, digest, or trailing bytes.
// Version handling stays native, including the legacy prefix without a minor
// version; separators and the two cost digits must retain their stored format.
// Neither the input nor a password is modified, and no expensive hashing runs.
// Errors describe malformed storage; Check adds the contract error identity.
func validatedCost(stored []byte) (int, error) {
	if len(stored) < len("$2$04$")+encodedSaltSize+encodedDigestSize {
		return 0, bcrypt.ErrHashTooShort
	}

	prefixSize := len("$2a$04$")

	if stored[2] == '$' {
		prefixSize = len("$2$04$")
	}

	if len(stored) != prefixSize+encodedSaltSize+encodedDigestSize {
		return 0, errors.New("invalid bcrypt record length")
	}

	cost, err := bcrypt.Cost(stored)

	if err != nil {
		return 0, err
	}

	if string(stored[prefixSize-len("$04$"):prefixSize]) != fmt.Sprintf("$%02d$", cost) {
		return 0, errors.New("invalid bcrypt cost field")
	}

	payload := stored[prefixSize:]

	if err := validateEncoding(payload[:encodedSaltSize]); err != nil {
		return 0, fmt.Errorf("invalid bcrypt salt: %w", err)
	}

	if err := validateEncoding(payload[encodedSaltSize:]); err != nil {
		return 0, fmt.Errorf("invalid bcrypt digest: %w", err)
	}

	return cost, nil
}

// validateEncoding requires canonical unpadded bcrypt base64 without changing
// encoded. Decoding checks the alphabet; re-encoding also rejects ignored line
// breaks and nonzero unused bits. The caller enforces the salt/digest lengths.
// Errors contain no stored bytes and require no password computation.
func validateEncoding(encoded []byte) error {
	encoding := base64.NewEncoding("./ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789").
		WithPadding(base64.NoPadding)
	decoded, err := encoding.DecodeString(string(encoded))

	if err != nil {
		return err
	}

	if encoding.EncodeToString(decoded) != string(encoded) {
		return errors.New("noncanonical bcrypt base64")
	}

	return nil
}
