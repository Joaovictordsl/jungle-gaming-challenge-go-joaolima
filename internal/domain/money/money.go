package money

import (
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

	if currency != "BRL" && currency != "USD" {
		return Money{}, errors.New("moeda não suportada.")
	}

	str := strings.Split(decimal, ".")

	if len(str) != 2 {
		return Money{}, errors.New("valor fora do escopo.")
	}

	if len(str[0]) == 0 {
		return Money{}, errors.New("valor fora do escopo.")
	}

	if len(str[1]) != 2 {
		return Money{}, errors.New("valor fora do escopo.")
	}

	dec := str[0] + str[1]

	for _, val := range dec {

		if val < '0' || val > '9' {
			return Money{}, errors.New("O valor não é um número.")
		}
	}

	cents, err := strconv.ParseInt(dec, 10, 64)
	if err != nil {
		return Money{}, errors.New("Erro de parse.")

	}

	return Money{cents: cents, currency: currency}, nil
}

func (m Money) Add(other Money) (Money, error) {

	if other.currency == "" || m.currency == "" {
		return Money{}, errors.New("Moeda vazia.")
	}

	if other.currency != m.currency {
		return Money{}, errors.New("Moeda errada.")

	}

	if other.cents > 0 && m.cents > math.MaxInt64-other.cents {
		return Money{}, errors.New("Valor Maior que o permitido.")

	}

	if other.cents < 0 && m.cents < math.MinInt64-other.cents {
		return Money{}, errors.New("Valor menor que o permitido.")

	}

	return Money{cents: other.cents + m.cents, currency: m.currency}, nil
}
