package payment

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Money represents a nonnegative amount in integer currency minor units.
// Zero is valid for observations; checkout creation and refunds require positive
// amounts. Currency is a supported uppercase ISO code, never inferred or rounded.
//
// Example:
//
//	price := payment.Money{Amount: 1250, Currency: "USD"}
//	decimal, err := price.Decimal()
//	if err != nil { return err }
//	_ = decimal // "12.50"
type Money struct {
	// Amount is the number of minor units, in [0, math.MaxInt64].
	// Conversion methods reject negative values instead of interpreting a debit.
	//
	// Example:
	//	price := payment.Money{Amount: 1250, Currency: "USD"}
	//	_ = price.Amount // 1250 cents
	Amount int64 `json:"amount"`

	// Currency is the uppercase code accepted by CurrencyExponent.
	// Providers may impose a stricter currency subset.
	//
	// Example:
	//	price := payment.Money{Amount: 1250, Currency: "USD"}
	//	_ = price.Currency // "USD"
	Currency string `json:"currency"`
}

// decimalPattern accepts unsigned decimal notation without exponent syntax,
// whitespace, or locale separators. Precision is checked separately per currency.
var decimalPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

// NewMoney validates an exact integer minor-unit amount without rounding or
// floating-point conversion. Zero is valid; negatives return ErrInvalidRequest
// and unknown currencies return ErrUnsupported. On failure it returns zero Money.
//
// Example:
//
//	amount, err := payment.NewMoney("USD", 1250)
//	if err != nil { return err }
//	_ = amount.Amount // 1250 cents
func NewMoney(currency string, minor int64) (Money, error) {
	money := Money{Amount: minor, Currency: currency}

	if err := money.Validate(); err != nil {
		return Money{}, err
	}

	return money, nil
}

// CurrencyExponent returns the reviewed currency's number of decimal places.
// Unknown codes, including lowercase codes, wrap ErrUnsupported. There is no
// default exponent: adding a currency requires an explicit reviewed entry.
//
// Example:
//
//	exponent, err := payment.CurrencyExponent("JPY")
//	if err != nil { return err }
//	_ = exponent // 0
func CurrencyExponent(currency string) (int, error) {
	switch currency {
	case "USD", "EUR", "GBP", "CAD", "AUD", "NZD", "CHF", "CNY", "HKD", "SGD",
		"SEK", "NOK", "DKK", "PLN", "CZK", "HUF", "BRL", "MXN", "INR", "ZAR":
		return 2, nil
	case "JPY", "KRW":
		return 0, nil
	case "BHD", "KWD", "OMR":
		return 3, nil
	default:
		return 0, fmt.Errorf("%w: currency", ErrUnsupported)
	}
}

// Validate checks nonnegative minor units and the explicit currency vocabulary.
// It does not require a positive amount or validate provider-specific limits.
//
// Example:
//
//	if err := (payment.Money{Amount: 0, Currency: "USD"}).Validate(); err != nil {
//		return err
//	}
func (money Money) Validate() error {
	if money.Amount < 0 {
		return fmt.Errorf("%w: amount must be nonnegative", ErrInvalidRequest)
	}

	_, err := CurrencyExponent(money.Currency)

	return err
}

// Decimal formats minor units as exact unsigned decimal notation, retaining
// the currency's full precision. Invalid amounts return ErrInvalidRequest;
// unsupported currencies return ErrUnsupported. It never uses floating point.
//
// Example:
//
//	value, err := (payment.Money{Amount: 1234, Currency: "KWD"}).Decimal()
//	if err != nil { return err }
//	_ = value // "1.234"
func (money Money) Decimal() (string, error) {
	if money.Amount < 0 {
		return "", fmt.Errorf("%w: amount must be nonnegative", ErrInvalidRequest)
	}

	exponent, err := CurrencyExponent(money.Currency)

	if err != nil {
		return "", err
	}

	digits := strconv.FormatInt(money.Amount, 10)

	if exponent == 0 {
		return digits, nil
	}

	digits = strings.Repeat("0", max(0, exponent+1-len(digits))) + digits
	point := len(digits) - exponent

	return digits[:point] + "." + digits[point:], nil
}

// ParseMoney converts unsigned decimal notation to exact integer minor units.
// Missing fractional places are padded; excess places, signs, exponent notation,
// whitespace, and int64 overflow return ErrInvalidRequest. Unknown currencies
// return ErrUnsupported. No rounding or floating-point conversion occurs.
//
// Example:
//
//	price, err := payment.ParseMoney("USD", "12.5")
//	if err != nil { return err }
//	_ = price.Amount // 1250
func ParseMoney(currency, value string) (Money, error) {
	exponent, err := CurrencyExponent(currency)

	if err != nil {
		return Money{}, err
	}

	if !decimalPattern.MatchString(value) {
		return Money{}, fmt.Errorf("%w: decimal amount", ErrInvalidRequest)
	}

	whole, fraction, _ := strings.Cut(value, ".")

	if len(fraction) > exponent {
		return Money{}, fmt.Errorf("%w: decimal precision", ErrInvalidRequest)
	}

	digits := whole + fraction + strings.Repeat("0", exponent-len(fraction))

	amount, err := strconv.ParseInt(digits, 10, 64)

	if err != nil {
		return Money{}, fmt.Errorf("%w: amount overflow", ErrInvalidRequest)
	}

	return Money{Amount: amount, Currency: currency}, nil
}
