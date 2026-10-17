package money

import "testing"

func TestNewValidAmount(t *testing.T) {

	valor, err := New("35.05", "USD")
	if err != nil {
		t.Fatalf("Erro não esperado. %v", err)
	}

	if valor.MinorUnits() != 3505 {
		t.Fatalf("esperava 3505 centavos; recebeu %d", valor.MinorUnits())
	}

	if valor.Currency() != "USD" {
		t.Fatalf("Esperava USD; recebeu %s", valor.Currency())
	}

}

func TestNewRejectsInvalidScale(t *testing.T) {

	_, err := New("35.5", "BRL")
	if err == nil {
		t.Fatalf("Esperava erro por escala inválida: %v", err)
	}

}

func TestNewZeroAmount(t *testing.T) {

	valor, err := New("0.00", "BRL")
	if err != nil {
		t.Fatalf("Erro: %v", err)
	}

	if valor.MinorUnits() != 0 {
		t.Fatalf("Valor diferente de zero: %d", valor.MinorUnits())
	}

	if valor.Currency() != "BRL" {
		t.Fatalf("Erro de moeda: %s", valor.Currency())
	}

}

func TestNewRejectsNegativeAmount(t *testing.T) {

	valor, err := New("-1.00", "BRL")
	if err == nil {
		t.Fatalf("Número negativo: %v", valor)
	}

}

func TestNewRejectsOverflow(t *testing.T) {

	valor, err := New("92233720368547758.08", "BRL")
	if err == nil {
		t.Fatalf("Valor muito acima do permitido: %v", valor)
	}

}

func TestNewMaxAmount(t *testing.T) {

	valor, err := New("92233720368547758.07", "BRL")

	if err != nil {
		t.Fatalf("Erro: %v", err)
	}

	if valor.MinorUnits() != 9223372036854775807 {
		t.Fatalf("Esperava 9223372036854775807 centavos; recebeu %d", valor.MinorUnits())
	}

	if valor.Currency() != "BRL" {
		t.Fatalf("Esperava BRL; recebeu %s", valor.Currency())
	}

}

func TestAddSameCurrency(t *testing.T) {
	valor, err := New("35.05", "BRL")
	if err != nil {
		t.Fatalf("Erro ao criar o primeiro valor: %v", err)
	}

	outro, err := New("10.00", "BRL")
	if err != nil {
		t.Fatalf("Erro ao criar o segundo valor: %v", err)
	}

	resultado, err := valor.Add(outro)
	if err != nil {
		t.Fatalf("Erro inesperado na soma: %v", err)
	}

	if resultado.MinorUnits() != 4505 {
		t.Fatalf("Esperava 4505 centavos; recebeu %d", resultado.MinorUnits())
	}

	if resultado.Currency() != "BRL" {
		t.Fatalf("Esperava BRL; recebeu %s", resultado.Currency())
	}
}

func TestAddRejectsDifferentCurrencies(t *testing.T) {
	valor, err := New("10.00", "BRL")
	if err != nil {
		t.Fatalf("Erro ao criar o valor em BRL: %v", err)
	}

	outro, err := New("5.00", "USD")
	if err != nil {
		t.Fatalf("Erro ao criar o valor em USD: %v", err)
	}

	_, err = valor.Add(outro)
	if err == nil {
		t.Fatalf("Esperava erro ao somar valores de moedas diferentes")
	}
}
