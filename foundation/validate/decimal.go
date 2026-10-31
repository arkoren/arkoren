package validate

import (
	"math/big"
	"strconv"
)

// decimalMultiple applies JSON Schema's integer-quotient rule to the shortest
// decimal encodings of floating values. bits must be 32 or 64 and controls the
// same rounding as JSON's encoding of that Go float type. No tolerance is used:
// nearby decimal nonmultiples stay invalid. Nonfinite operands and zero factors
// return false; construction owns the factor's positivity checks.
func decimalMultiple(value, factor float64, bits int) bool {
	divisor, ok := new(big.Rat).SetString(strconv.FormatFloat(factor, 'g', -1, bits))

	if !ok || divisor.Sign() == 0 {
		return false
	}

	dividend, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, bits))

	if !ok {
		return false
	}

	return new(big.Rat).Quo(dividend, divisor).IsInt()
}
