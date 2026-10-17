package money

import "testing"

func TestNewValidAmount(t *testing.T) {

	valor, err := New("35.05", "USD")
	if err != nil {
		t.Fatalf("Erro não esperado.")
	}

	if valor.MinorUnits() != 3505 {
		t.Fatalf("esperava 3505 centavos; recebeu %d", valor.MinorUnits())
	}

	if valor.Currency() != "USD" {
		t.Fatalf("Esperava USD ou BRL; recebeu %s", valor.Currency())
	}

}
