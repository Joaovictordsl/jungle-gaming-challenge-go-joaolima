package money

import (
	"errors"
	"math"
	"testing"
)

func TestFromMinorUnitsPreservesInternalValues(t *testing.T) {
	for _, currency := range []string{"BRL", "USD"} {
		for _, cents := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64} {
			value, err := FromMinorUnits(cents, currency)
			if err != nil {
				t.Fatalf("Erro inesperado ao reconstruir Money: %v", err)
			}
			if value.MinorUnits() != cents || value.Currency() != currency {
				t.Fatalf("Reconstrução alterou o valor interno %d %s", cents, currency)
			}
		}
	}
}

func TestFromMinorUnitsRejectsInvalidCurrency(t *testing.T) {
	for _, currency := range []string{"", "EUR", "brl"} {
		value, err := FromMinorUnits(100, currency)
		if !errors.Is(err, ErrInvalidCurrency) || value != (Money{}) {
			t.Fatalf("Moeda inválida deve retornar ErrInvalidCurrency e valor vazio: %v", err)
		}
	}
}
