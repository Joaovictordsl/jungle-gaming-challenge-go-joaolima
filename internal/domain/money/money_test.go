package money

import (
	"math"
	"testing"
)

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

func TestAddRejectsOverflow(t *testing.T) {
	valor, err := New("92233720368547758.07", "BRL")
	if err != nil {
		t.Fatalf("Erro ao criar o valor máximo: %v", err)
	}

	outro, err := New("0.01", "BRL")
	if err != nil {
		t.Fatalf("Erro ao criar o valor de um centavo: %v", err)
	}

	_, err = valor.Add(outro)
	if err == nil {
		t.Fatalf("Esperava erro de overflow ao somar um centavo ao valor máximo")
	}
}

func TestSubtractAmounts(t *testing.T) {
	casos := []struct {
		nome     string
		valor    string
		outro    string
		esperado int64
	}{
		{nome: "resultado positivo", valor: "35.05", outro: "10.00", esperado: 2505},
		{nome: "resultado zero", valor: "10.00", outro: "10.00", esperado: 0},
		{nome: "resultado negativo", valor: "10.00", outro: "15.00", esperado: -500},
		{nome: "subtrair zero", valor: "35.05", outro: "0.00", esperado: 3505},
	}

	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			valor, err := New(caso.valor, "BRL")
			if err != nil {
				t.Fatalf("Erro ao criar o primeiro valor: %v", err)
			}

			outro, err := New(caso.outro, "BRL")
			if err != nil {
				t.Fatalf("Erro ao criar o segundo valor: %v", err)
			}

			valorOriginal, outroOriginal := valor, outro
			resultado, err := valor.Subtract(outro)
			if err != nil {
				t.Fatalf("Erro inesperado na subtração: %v", err)
			}

			if resultado.MinorUnits() != caso.esperado {
				t.Fatalf("Esperava %d centavos; recebeu %d", caso.esperado, resultado.MinorUnits())
			}
			if resultado.Currency() != "BRL" {
				t.Fatalf("Esperava BRL; recebeu %s", resultado.Currency())
			}
			if valor != valorOriginal || outro != outroOriginal {
				t.Fatalf("A subtração alterou um dos valores originais")
			}
		})
	}
}

func TestSubtractRejectsInvalidCurrencies(t *testing.T) {
	casos := []struct {
		nome  string
		valor Money
		outro Money
	}{
		{nome: "primeiro valor não inicializado", valor: Money{}, outro: Money{cents: 100, currency: "BRL"}},
		{nome: "segundo valor não inicializado", valor: Money{cents: 100, currency: "BRL"}, outro: Money{}},
		{nome: "ambos não inicializados", valor: Money{}, outro: Money{}},
		{nome: "moedas diferentes", valor: Money{cents: 1000, currency: "BRL"}, outro: Money{cents: 500, currency: "USD"}},
	}

	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			_, err := caso.valor.Subtract(caso.outro)
			if err == nil {
				t.Fatalf("Esperava erro para moedas vazias ou incompatíveis")
			}
		})
	}
}

func TestSubtractInt64Limits(t *testing.T) {
	// Os valores negativos são estados internos válidos; New rejeita entradas externas negativas.
	casos := []struct {
		nome       string
		valor      int64
		outro      int64
		esperado   int64
		esperaErro bool
	}{
		{nome: "atingir mínimo", valor: math.MinInt64 + 1, outro: 1, esperado: math.MinInt64},
		{nome: "atingir máximo", valor: math.MaxInt64 - 1, outro: -1, esperado: math.MaxInt64},
		{nome: "abaixo do mínimo", valor: math.MinInt64, outro: 1, esperaErro: true},
		{nome: "acima do máximo", valor: math.MaxInt64, outro: -1, esperaErro: true},
		{nome: "zero menos mínimo excede máximo", valor: 0, outro: math.MinInt64, esperaErro: true},
		{nome: "menos um menos mínimo", valor: -1, outro: math.MinInt64, esperado: math.MaxInt64},
		{nome: "mínimo menos mínimo", valor: math.MinInt64, outro: math.MinInt64, esperado: 0},
		{nome: "máximo menos máximo", valor: math.MaxInt64, outro: math.MaxInt64, esperado: 0},
	}

	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			valor := Money{cents: caso.valor, currency: "BRL"}
			outro := Money{cents: caso.outro, currency: "BRL"}
			resultado, err := valor.Subtract(outro)
			if caso.esperaErro {
				if err == nil {
					t.Fatalf("Esperava erro de overflow na subtração")
				}
				return
			}

			if err != nil {
				t.Fatalf("Erro inesperado na subtração: %v", err)
			}
			if resultado.MinorUnits() != caso.esperado {
				t.Fatalf("Esperava %d centavos; recebeu %d", caso.esperado, resultado.MinorUnits())
			}
			if resultado.Currency() != "BRL" {
				t.Fatalf("Esperava BRL; recebeu %s", resultado.Currency())
			}
		})
	}
}

func TestNegate(t *testing.T) {
	casos := []struct {
		nome       string
		valor      Money
		esperado   int64
		esperaErro bool
	}{
		{nome: "positivo vira negativo", valor: Money{cents: 3505, currency: "BRL"}, esperado: -3505},
		{nome: "negativo vira positivo", valor: Money{cents: -3505, currency: "BRL"}, esperado: 3505},
		{nome: "zero preserva moeda", valor: Money{cents: 0, currency: "USD"}, esperado: 0},
		{nome: "máximo vira negativo", valor: Money{cents: math.MaxInt64, currency: "BRL"}, esperado: -math.MaxInt64},
		{nome: "mínimo mais um vira máximo", valor: Money{cents: math.MinInt64 + 1, currency: "BRL"}, esperado: math.MaxInt64},
		{nome: "mínimo causa overflow", valor: Money{cents: math.MinInt64, currency: "BRL"}, esperaErro: true},
		{nome: "valor não inicializado", valor: Money{}, esperaErro: true},
		{nome: "centavos sem moeda", valor: Money{cents: 3505}, esperaErro: true},
	}

	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			original := caso.valor
			resultado, err := caso.valor.Negate()
			if caso.valor != original {
				t.Fatalf("A negação alterou o valor original")
			}

			if caso.esperaErro {
				if err == nil {
					t.Fatalf("Esperava erro ao negar o valor")
				}
				return
			}

			if err != nil {
				t.Fatalf("Erro inesperado na negação: %v", err)
			}
			if resultado.MinorUnits() != caso.esperado {
				t.Fatalf("Esperava %d centavos; recebeu %d", caso.esperado, resultado.MinorUnits())
			}
			if resultado.Currency() != original.Currency() {
				t.Fatalf("Esperava moeda %s; recebeu %s", original.Currency(), resultado.Currency())
			}
		})
	}
}
