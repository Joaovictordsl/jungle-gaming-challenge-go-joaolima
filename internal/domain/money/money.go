package money

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
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

		if !unicode.IsNumber(val) {
			return Money{}, errors.New("O valor não é um número.")
		}
	}

	cents, err := strconv.ParseInt(dec, 10, 64)
	if err != nil {
		return Money{}, errors.New("Erro de parse.")

	}

	return Money{cents: cents, currency: currency}, nil
}
