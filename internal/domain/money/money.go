package money

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
)

type Money struct {
	cents    int64
	currency string
}

func (m Money) MinorUnits() int64 {
	return m.cents
}

func (m Money) Currency() string {
	return m.currency
}

func New(decimal string, currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}

	str := strings.Split(decimal, ".")

	if len(str) != 2 {
		return Money{}, ErrInvalidAmount
	}

	if len(str[0]) == 0 {
		return Money{}, ErrInvalidAmount
	}

	if len(str[1]) != 2 {
		return Money{}, ErrInvalidAmount
	}

	dec := str[0] + str[1]

	for _, val := range dec {
		if val < '0' || val > '9' {
			return Money{}, ErrInvalidAmount
		}
	}

	cents, err := strconv.ParseInt(dec, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return Money{}, ErrOverflow
		}
		return Money{}, ErrInvalidAmount
	}

	return Money{cents: cents, currency: currency}, nil
}

// FromMinorUnits reconstrói um valor interno a partir de centavos, como os de uma
// coluna BIGINT. Entradas financeiras externas devem continuar usando New.
func FromMinorUnits(cents int64, currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	return Money{cents: cents, currency: currency}, nil
}

// Zero cria um zero válido na moeda solicitada.
func Zero(currency string) (Money, error) {
	if err := validateCurrency(currency); err != nil {
		return Money{}, err
	}
	return Money{cents: 0, currency: currency}, nil
}

// Validate diferencia um valor monetário válido do valor zero da struct.
func (m Money) Validate() error {
	if m.currency == "" {
		return ErrUninitialized
	}
	return validateCurrency(m.currency)
}

func validateCurrency(currency string) error {
	if currency != "BRL" && currency != "USD" {
		return ErrInvalidCurrency
	}
	return nil
}

func (m Money) compatibleWith(other Money) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := other.Validate(); err != nil {
		return err
	}
	if m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.compatibleWith(other); err != nil {
		return Money{}, err
	}

	if other.cents > 0 && m.cents > math.MaxInt64-other.cents {
		return Money{}, ErrOverflow
	}

	if other.cents < 0 && m.cents < math.MinInt64-other.cents {
		return Money{}, ErrOverflow
	}

	return Money{cents: other.cents + m.cents, currency: m.currency}, nil
}

func (m Money) Subtract(other Money) (Money, error) {
	if err := m.compatibleWith(other); err != nil {
		return Money{}, err
	}

	if other.cents > 0 && m.cents < math.MinInt64+other.cents {
		return Money{}, ErrOverflow
	}

	if other.cents < 0 && m.cents > math.MaxInt64+other.cents {
		return Money{}, ErrOverflow
	}

	return Money{cents: m.cents - other.cents, currency: m.currency}, nil
}

func (m Money) Negate() (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}

	if m.cents == math.MinInt64 {
		return Money{}, ErrOverflow
	}

	return Money{cents: -m.cents, currency: m.currency}, nil
}

// Compare retorna -1, 0 ou 1 conforme m seja menor, igual ou maior que other.
func (m Money) Compare(other Money) (int, error) {
	if err := m.compatibleWith(other); err != nil {
		return 0, err
	}
	if m.cents < other.cents {
		return -1, nil
	}
	if m.cents > other.cents {
		return 1, nil
	}
	return 0, nil
}

// Amount devolve o valor decimal canônico, inclusive para negativos internos.
func (m Money) Amount() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}

	// Trabalhar com o texto evita negar math.MinInt64, que causaria overflow.
	digits := strconv.FormatInt(m.cents, 10)
	sign := ""
	if digits[0] == '-' {
		sign = "-"
		digits = digits[1:]
	}
	if len(digits) < 3 {
		digits = strings.Repeat("0", 3-len(digits)) + digits
	}
	point := len(digits) - 2
	return sign + digits[:point] + "." + digits[point:], nil
}

// MarshalJSON mantém amount como string, sem passar por ponto flutuante.
func (m Money) MarshalJSON() ([]byte, error) {
	amount, err := m.Amount()
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}{Amount: amount, Currency: m.currency})
}
