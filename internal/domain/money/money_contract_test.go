package money

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestNewRejectsMalformedAmounts(t *testing.T) {
	inputs := []string{
		"", "25", ".25", "25.", "25.5", "25.000", "25.00.00",
		"-1.00", "-0.00", "+1.00", " 1.00", "1.00 ", "1 .00",
		"1,00", "NaN", "Infinity", "1e2", "1e2.00", "a.00", "1.ab",
		"١.00", "1.０0", "1.\n0",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, err := New(input, "BRL")
			if !errors.Is(err, ErrInvalidAmount) {
				t.Fatalf("New(%q): expected ErrInvalidAmount; got %v", input, err)
			}
		})
	}
}

func TestNewRejectsInvalidCurrencies(t *testing.T) {
	for _, currency := range []string{"", "EUR", "brl", "usd", " BRL", "BRL "} {
		t.Run(currency, func(t *testing.T) {
			_, err := New("1.00", currency)
			if !errors.Is(err, ErrInvalidCurrency) {
				t.Fatalf("expected ErrInvalidCurrency; got %v", err)
			}
		})
	}
}

func TestNewNormalizesLeadingZeros(t *testing.T) {
	cases := []struct {
		input    string
		currency string
		cents    int64
		amount   string
	}{
		{"00035.05", "BRL", 3505, "35.05"},
		{"000.00", "USD", 0, "0.00"},
		{"0000.01", "BRL", 1, "0.01"},
		{"00092233720368547758.07", "USD", math.MaxInt64, "92233720368547758.07"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			value, err := New(tc.input, tc.currency)
			if err != nil {
				t.Fatalf("unexpected construction error: %v", err)
			}
			amount, err := value.Amount()
			if err != nil {
				t.Fatalf("unexpected serialization error: %v", err)
			}
			if value.MinorUnits() != tc.cents || value.Currency() != tc.currency || amount != tc.amount {
				t.Fatalf("got cents=%d currency=%q amount=%q; expected %d %q %q", value.MinorUnits(), value.Currency(), amount, tc.cents, tc.currency, tc.amount)
			}
		})
	}

	_, err := New("92233720368547758.08", "BRL")
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow beyond maximum; got %v", err)
	}
}

func TestZeroByCurrency(t *testing.T) {
	for _, currency := range []string{"BRL", "USD"} {
		t.Run(currency, func(t *testing.T) {
			value, err := Zero(currency)
			if err != nil {
				t.Fatalf("unexpected construction error: %v", err)
			}
			if value.MinorUnits() != 0 || value.Currency() != currency {
				t.Fatalf("expected initialized zero %s; got %#v", currency, value)
			}
			if err := value.Validate(); err != nil {
				t.Fatalf("zero must be valid: %v", err)
			}
			amount, err := value.Amount()
			if err != nil || amount != "0.00" {
				t.Fatalf("expected 0.00; got %q, %v", amount, err)
			}
		})
	}
	for _, currency := range []string{"", "EUR", "brl"} {
		_, err := Zero(currency)
		if !errors.Is(err, ErrInvalidCurrency) {
			t.Fatalf("Zero(%q): expected ErrInvalidCurrency; got %v", currency, err)
		}
	}
}

func TestValidateMoney(t *testing.T) {
	cases := []struct {
		name  string
		value Money
		want  error
	}{
		{"uninitialized", Money{}, ErrUninitialized},
		{"cents without currency", Money{cents: 100}, ErrUninitialized},
		{"unsupported currency", Money{currency: "EUR"}, ErrInvalidCurrency},
		{"initialized zero", Money{currency: "BRL"}, nil},
		{"negative internal value", Money{cents: math.MinInt64, currency: "USD"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.value.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("expected %v; got %v", tc.want, err)
			}
		})
	}
}

func TestBinaryOperationsClassifyCurrencyErrors(t *testing.T) {
	cases := []struct {
		name  string
		left  Money
		right Money
		want  error
	}{
		{"left uninitialized", Money{}, Money{currency: "BRL"}, ErrUninitialized},
		{"right uninitialized", Money{currency: "BRL"}, Money{}, ErrUninitialized},
		{"both uninitialized", Money{}, Money{}, ErrUninitialized},
		{"left invalid currency", Money{currency: "EUR"}, Money{currency: "BRL"}, ErrInvalidCurrency},
		{"right invalid currency", Money{currency: "BRL"}, Money{currency: "EUR"}, ErrInvalidCurrency},
		{"same invalid currency", Money{currency: "EUR"}, Money{currency: "EUR"}, ErrInvalidCurrency},
		{"different valid currencies", Money{currency: "BRL"}, Money{currency: "USD"}, ErrCurrencyMismatch},
	}
	operations := []struct {
		name string
		run  func(Money, Money) error
	}{
		{"Add", func(left, right Money) error { _, err := left.Add(right); return err }},
		{"Subtract", func(left, right Money) error { _, err := left.Subtract(right); return err }},
		{"Compare", func(left, right Money) error { _, err := left.Compare(right); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					leftBefore, rightBefore := tc.left, tc.right
					if err := operation.run(tc.left, tc.right); !errors.Is(err, tc.want) {
						t.Fatalf("expected %v; got %v", tc.want, err)
					}
					if tc.left != leftBefore || tc.right != rightBefore {
						t.Fatal("operation changed an operand")
					}
				})
			}
		})
	}
}

func TestAddSignedAmountsAndLimits(t *testing.T) {
	cases := []struct {
		name  string
		left  int64
		right int64
		want  int64
		err   error
	}{
		{"zero identity", 3505, 0, 3505, nil},
		{"negative sum", -3505, -100, -3605, nil},
		{"opposite values", -3505, 3505, 0, nil},
		{"reach maximum", math.MaxInt64 - 1, 1, math.MaxInt64, nil},
		{"reach minimum", math.MinInt64 + 1, -1, math.MinInt64, nil},
		{"zero plus minimum", 0, math.MinInt64, math.MinInt64, nil},
		{"extremes cancel to minus one", math.MaxInt64, math.MinInt64, -1, nil},
		{"overflow above maximum", math.MaxInt64, 1, 0, ErrOverflow},
		{"overflow below minimum", math.MinInt64, -1, 0, ErrOverflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			left := Money{cents: tc.left, currency: "USD"}
			right := Money{cents: tc.right, currency: "USD"}
			leftBefore, rightBefore := left, right
			result, err := left.Add(right)
			if !errors.Is(err, tc.err) {
				t.Fatalf("expected error %v; got %v", tc.err, err)
			}
			if tc.err == nil && (result.MinorUnits() != tc.want || result.Currency() != "USD") {
				t.Fatalf("expected %d cents USD; got %#v", tc.want, result)
			}
			if left != leftBefore || right != rightBefore {
				t.Fatal("addition changed an operand")
			}
		})
	}
}

func TestCompareSignedAmounts(t *testing.T) {
	cases := []struct {
		name  string
		left  int64
		right int64
		want  int
	}{
		{"less", 100, 200, -1},
		{"equal", 100, 100, 0},
		{"greater", 200, 100, 1},
		{"negative less", -200, -100, -1},
		{"negative equal", -100, -100, 0},
		{"zero equal", 0, 0, 0},
		{"maximum versus minimum", math.MaxInt64, math.MinInt64, 1},
		{"minimum versus maximum", math.MinInt64, math.MaxInt64, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			left := Money{cents: tc.left, currency: "BRL"}
			right := Money{cents: tc.right, currency: "BRL"}
			leftBefore, rightBefore := left, right
			result, err := left.Compare(right)
			if err != nil || result != tc.want {
				t.Fatalf("expected comparison %d; got %d, %v", tc.want, result, err)
			}
			if left != leftBefore || right != rightBefore {
				t.Fatal("comparison changed an operand")
			}
		})
	}
}

func TestAmountAndJSONExactRepresentation(t *testing.T) {
	cases := []struct {
		name   string
		cents  int64
		amount string
	}{
		{"zero", 0, "0.00"},
		{"one cent", 1, "0.01"},
		{"fraction padded", 105, "1.05"},
		{"whole amount", 3500, "35.00"},
		{"negative cent", -1, "-0.01"},
		{"negative fraction", -99, "-0.99"},
		{"negative whole", -100, "-1.00"},
		{"negative amount", -3505, "-35.05"},
		{"maximum", math.MaxInt64, "92233720368547758.07"},
		{"minimum", math.MinInt64, "-92233720368547758.08"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := Money{cents: tc.cents, currency: "BRL"}
			before := value
			amount, err := value.Amount()
			if err != nil || amount != tc.amount {
				t.Fatalf("expected amount %q; got %q, %v", tc.amount, amount, err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("unexpected JSON error: %v", err)
			}
			// Decoding to string fields also rejects an amount emitted as a JSON number.
			var fields map[string]string
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatalf("expected string-valued JSON fields: %s: %v", encoded, err)
			}
			if len(fields) != 2 || fields["amount"] != tc.amount || fields["currency"] != "BRL" {
				t.Fatalf("unexpected JSON contract: %s", encoded)
			}
			if value != before {
				t.Fatal("serialization changed the original value")
			}
		})
	}
}

func TestUnaryOperationsClassifyErrors(t *testing.T) {
	operations := []struct {
		name string
		run  func(Money) error
	}{
		{"Negate", func(value Money) error { _, err := value.Negate(); return err }},
		{"Amount", func(value Money) error { _, err := value.Amount(); return err }},
		{"MarshalJSON", func(value Money) error { _, err := json.Marshal(value); return err }},
	}
	cases := []struct {
		name  string
		value Money
		want  error
	}{
		{"uninitialized", Money{}, ErrUninitialized},
		{"invalid currency", Money{cents: 1, currency: "EUR"}, ErrInvalidCurrency},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					before := tc.value
					if err := operation.run(tc.value); !errors.Is(err, tc.want) {
						t.Fatalf("expected %v; got %v", tc.want, err)
					}
					if tc.value != before {
						t.Fatal("operation changed the original value")
					}
				})
			}
		})
	}
	_, err := (Money{cents: math.MinInt64, currency: "BRL"}).Negate()
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("Negate minimum: expected ErrOverflow; got %v", err)
	}

}

func TestSubtractClassifiesOverflow(t *testing.T) {
	cases := []struct {
		name  string
		left  int64
		right int64
	}{
		{"below minimum", math.MinInt64, 1},
		{"above maximum", math.MaxInt64, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			left := Money{cents: tc.left, currency: "BRL"}
			right := Money{cents: tc.right, currency: "BRL"}
			_, err := left.Subtract(right)
			if !errors.Is(err, ErrOverflow) {
				t.Fatalf("expected ErrOverflow; got %v", err)
			}
		})
	}
}
