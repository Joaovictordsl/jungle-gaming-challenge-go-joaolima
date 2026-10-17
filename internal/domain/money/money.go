package money

import (
	"errors"
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

	return Money{}, nil
}
