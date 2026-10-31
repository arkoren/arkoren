package payment_test

import (
	"errors"
	"math"
	"testing"

	"arkoren.dev/core/service/payment"
)

func TestMoneyDecimalRetainsTwoPlaces(t *testing.T) {
	t.Parallel()

	value, err := (payment.Money{Amount: 1, Currency: "USD"}).Decimal()

	if err != nil {
		t.Fatal(err)
	}

	if value != "0.01" {
		t.Fatalf("decimal = %q, want 0.01", value)
	}
}

func TestMoneyDecimalSupportsZeroExponent(t *testing.T) {
	t.Parallel()

	value, err := (payment.Money{Amount: 123, Currency: "JPY"}).Decimal()

	if err != nil {
		t.Fatal(err)
	}

	if value != "123" {
		t.Fatalf("decimal = %q, want 123", value)
	}
}

func TestMoneyDecimalSupportsMaximumInt64(t *testing.T) {
	t.Parallel()

	value, err := (payment.Money{Amount: math.MaxInt64, Currency: "KWD"}).Decimal()

	if err != nil {
		t.Fatal(err)
	}

	if value != "9223372036854775.807" {
		t.Fatalf("decimal = %q", value)
	}

	money, err := payment.ParseMoney("KWD", value)

	if err != nil {
		t.Fatal(err)
	}

	if money.Amount != math.MaxInt64 {
		t.Fatalf("round-trip amount = %d", money.Amount)
	}
}

func TestMoneyZeroWithoutFraction(t *testing.T) {
	t.Parallel()

	value, err := (payment.Money{Currency: "JPY"}).Decimal()

	if err != nil {
		t.Fatal(err)
	}

	if value != "0" {
		t.Fatalf("decimal = %q, want 0", value)
	}

	money, err := payment.ParseMoney("JPY", value)

	if err != nil {
		t.Fatal(err)
	}

	if money != (payment.Money{Currency: "JPY"}) {
		t.Fatalf("round-trip money = %+v", money)
	}
}

func TestMoneyZeroWithThreePlaces(t *testing.T) {
	t.Parallel()

	value, err := (payment.Money{Currency: "BHD"}).Decimal()

	if err != nil {
		t.Fatal(err)
	}

	if value != "0.000" {
		t.Fatalf("decimal = %q, want 0.000", value)
	}

	money, err := payment.ParseMoney("BHD", value)

	if err != nil {
		t.Fatal(err)
	}

	if money != (payment.Money{Currency: "BHD"}) {
		t.Fatalf("round-trip money = %+v", money)
	}
}

func TestMoneyZeroIsValid(t *testing.T) {
	t.Parallel()

	money := payment.Money{Currency: "USD"}

	if err := money.Validate(); err != nil {
		t.Fatal(err)
	}

	value, err := money.Decimal()

	if err != nil {
		t.Fatal(err)
	}

	if value != "0.00" {
		t.Fatalf("decimal = %q, want 0.00", value)
	}

	parsed, err := payment.ParseMoney("USD", value)

	if err != nil {
		t.Fatal(err)
	}

	if parsed != money {
		t.Fatalf("round-trip money = %+v", parsed)
	}
}

func TestMoneyRejectsNegativeAmounts(t *testing.T) {
	t.Parallel()

	money := payment.Money{Amount: -1, Currency: "USD"}

	if err := money.Validate(); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("Validate = %v", err)
	}

	if _, err := money.Decimal(); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("Decimal = %v", err)
	}
}

func TestMoneyRejectsUnknownCurrency(t *testing.T) {
	t.Parallel()

	for _, currency := range []string{"", "usd", "XYZ", "USDT"} {
		money := payment.Money{Amount: 1, Currency: currency}

		if err := money.Validate(); !errors.Is(err, payment.ErrUnsupported) {
			t.Fatalf("Validate(%q) = %v", currency, err)
		}

		if _, err := money.Decimal(); !errors.Is(err, payment.ErrUnsupported) {
			t.Fatalf("Decimal(%q) = %v", currency, err)
		}
	}
}

func TestCurrencyExponentReviewedTwoPlaceCurrencies(t *testing.T) {
	t.Parallel()

	for _, currency := range []string{"USD", "EUR", "GBP", "CAD", "AUD", "NZD", "CHF", "CNY", "HKD", "SGD",
		"SEK", "NOK", "DKK", "PLN", "CZK", "HUF", "BRL", "MXN", "INR", "ZAR"} {
		exponent, err := payment.CurrencyExponent(currency)

		if err != nil {
			t.Fatal(err)
		}

		if exponent != 2 {
			t.Fatalf("exponent(%q) = %d, want 2", currency, exponent)
		}
	}
}

func TestCurrencyExponentReviewedZeroPlaceCurrencies(t *testing.T) {
	t.Parallel()

	for _, currency := range []string{"JPY", "KRW"} {
		exponent, err := payment.CurrencyExponent(currency)

		if err != nil {
			t.Fatal(err)
		}

		if exponent != 0 {
			t.Fatalf("exponent(%q) = %d, want 0", currency, exponent)
		}
	}
}

func TestCurrencyExponentReviewedThreePlaceCurrencies(t *testing.T) {
	t.Parallel()

	for _, currency := range []string{"BHD", "KWD", "OMR"} {
		exponent, err := payment.CurrencyExponent(currency)

		if err != nil {
			t.Fatal(err)
		}

		if exponent != 3 {
			t.Fatalf("exponent(%q) = %d, want 3", currency, exponent)
		}
	}
}

func TestParseMoneyPadsMissingFraction(t *testing.T) {
	t.Parallel()

	money, err := payment.ParseMoney("USD", "0012.5")

	if err != nil {
		t.Fatal(err)
	}

	if money != (payment.Money{Amount: 1250, Currency: "USD"}) {
		t.Fatalf("money = %+v", money)
	}
}

func TestParseMoneyMaximumInt64(t *testing.T) {
	t.Parallel()

	money, err := payment.ParseMoney("USD", "92233720368547758.07")

	if err != nil {
		t.Fatal(err)
	}

	if money.Amount != math.MaxInt64 {
		t.Fatalf("amount = %d", money.Amount)
	}
}

func TestParseMoneyThreePlaces(t *testing.T) {
	t.Parallel()

	money, err := payment.ParseMoney("BHD", "1.234")

	if err != nil {
		t.Fatal(err)
	}

	if money.Amount != 1234 {
		t.Fatalf("amount = %d, want 1234", money.Amount)
	}
}

func TestParseMoneyRejectsInvalidDecimal(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "-1", "+1", "1e2", "1.", ".1", " 1", "1 ", "1,00", "１", "1.001",
		"92233720368547758.08", "999999999999999999999999999999999999999999"} {
		if _, err := payment.ParseMoney("USD", value); !errors.Is(err, payment.ErrInvalidRequest) {
			t.Fatalf("ParseMoney(%q) = %v", value, err)
		}
	}
}

func TestParseMoneyRejectsFractionForZeroExponent(t *testing.T) {
	t.Parallel()

	if _, err := payment.ParseMoney("JPY", "1.0"); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("ParseMoney = %v", err)
	}
}

func TestParseMoneyRejectsZeroExponentOverflow(t *testing.T) {
	t.Parallel()

	if _, err := payment.ParseMoney("JPY", "9223372036854775808"); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("ParseMoney = %v", err)
	}
}

func TestParseMoneyRejectsThreePlaceOverflow(t *testing.T) {
	t.Parallel()

	if _, err := payment.ParseMoney("KWD", "9223372036854775.808"); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("ParseMoney = %v", err)
	}
}

func TestParseMoneyRejectsExcessThreePlacePrecision(t *testing.T) {
	t.Parallel()

	if _, err := payment.ParseMoney("BHD", "0.0000"); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatalf("ParseMoney = %v", err)
	}
}

func TestParseMoneyRejectsUnknownCurrency(t *testing.T) {
	t.Parallel()

	if _, err := payment.ParseMoney("XYZ", "1"); !errors.Is(err, payment.ErrUnsupported) {
		t.Fatalf("ParseMoney = %v", err)
	}
}

func TestForintKeepsCanonicalMinorUnits(t *testing.T) {
	t.Parallel()

	amount := payment.Money{Amount: 1234, Currency: "HUF"}

	value, err := amount.Decimal()

	if err != nil {
		t.Fatal(err)
	}

	if value != "12.34" {
		t.Fatalf("decimal = %q, want 12.34", value)
	}

	parsed, err := payment.ParseMoney("HUF", "12")

	if err != nil {
		t.Fatal(err)
	}

	if parsed.Amount != 1200 {
		t.Fatalf("minor units = %d, want 1200", parsed.Amount)
	}
}

// TestNewMoneyRetainsExactMinorUnits checks constructor validation without
// changing Money's plain fields, rounding, or observation-friendly zero amounts.
func TestNewMoneyRetainsExactMinorUnits(t *testing.T) {
	t.Parallel()

	money, err := payment.NewMoney("KWD", 1234)

	if err != nil || money != (payment.Money{Amount: 1234, Currency: "KWD"}) {
		t.Fatal("money changed")
	}

	zero, err := payment.NewMoney("USD", 0)

	if err != nil || zero.Amount != 0 {
		t.Fatal("zero observation rejected")
	}

	if _, err := payment.NewMoney("USD", -1); !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("negative accepted")
	}

	if _, err := payment.NewMoney("unknown", 1); !errors.Is(err, payment.ErrUnsupported) {
		t.Fatal("currency identity changed")
	}
}
