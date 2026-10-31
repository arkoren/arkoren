package paypal

import (
	"fmt"
	"strconv"

	"arkoren.dev/core/service/payment"
)

// Decimal formats validated canonical minor units for PayPal's wallet currency
// subset. HUF keeps ISO scale two in core but PayPal requires whole forints;
// fractional forints fail locally rather than being rounded. Account/country
// restrictions remain provider policy. All other currencies use core precision.
// Example: value, err := paypal.Decimal(payment.Money{Amount: 1000, Currency: "HUF"}); _ = value; _ = err // "10"
func Decimal(money payment.Money) (string, error) {
	if err := checkCurrency(money.Currency); err != nil {
		return "", err
	}

	if money.Currency != "HUF" {
		return money.Decimal()
	}

	if money.Amount < 0 || money.Amount%100 != 0 {
		return "", fmt.Errorf("%w: paypal requires whole HUF", payment.ErrInvalidRequest)
	}

	return strconv.FormatInt(money.Amount/100, 10), nil
}

// checkCurrency admits only the reviewed intersection of PayPal wallet and
// canonical core currencies. There is no fallback to a two-decimal encoding.
func checkCurrency(currency string) error {
	switch currency {
	case "USD", "EUR", "GBP", "AUD", "CAD", "CHF", "CZK", "DKK", "HKD", "HUF", "JPY",
		"MXN", "NOK", "NZD", "PLN", "SEK", "SGD", "BRL", "CNY":
		return nil
	default:
		return fmt.Errorf("%w: paypal currency", payment.ErrUnsupported)
	}
}
