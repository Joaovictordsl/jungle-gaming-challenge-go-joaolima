package ledger

import (
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

func TestEntryValidatesFinancialEquation(t *testing.T) {
	value := func(amount, currency string) money.Money {
		t.Helper()
		v, err := money.New(amount, currency)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	zero := value("0.00", "BRL")
	ten := value("10.00", "BRL")
	twenty := value("20.00", "BRL")
	maximum := value("92233720368547758.07", "BRL")
	negative, _ := ten.Negate()
	now := time.Date(2026, 10, 18, 13, 30, 0, 0, time.UTC)
	cases := []struct {
		name, direction       string
		amount, before, after money.Money
		want                  error
	}{
		{name: "crédito", direction: Credit, amount: ten, before: ten, after: twenty},
		{name: "débito", direction: Debit, amount: ten, before: ten, after: zero},
		{name: "limite máximo", direction: Credit, amount: maximum, before: zero, after: maximum},
		{name: "saldo incorreto", direction: Credit, amount: ten, before: ten, after: ten, want: ErrInvalidEntry},
		{name: "moeda da movimentação", direction: Credit, amount: value("10.00", "USD"), before: zero, after: ten, want: money.ErrCurrencyMismatch},
		{name: "moeda do saldo final", direction: Credit, amount: ten, before: zero, after: value("10.00", "USD"), want: money.ErrCurrencyMismatch},
		{name: "overflow", direction: Credit, amount: ten, before: maximum, after: maximum, want: money.ErrOverflow},
		{name: "valor zero", direction: Credit, amount: zero, before: zero, after: zero, want: ErrInvalidEntry},
		{name: "saldo negativo", direction: Debit, amount: ten, before: zero, after: negative, want: ErrInvalidEntry},
		{name: "não inicializado", direction: Credit, amount: money.Money{}, before: zero, after: ten, want: money.ErrUninitialized},
		{name: "direção inválida", direction: "OTHER", amount: ten, before: zero, after: ten, want: ErrInvalidEntry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := New("entry-1", "wallet-1", "transaction-1", tc.direction, tc.amount, tc.before, tc.after, now)
			if tc.want != nil {
				if !errors.Is(err, tc.want) || e != (Entry{}) {
					t.Fatalf("Esperava lançamento vazio e %v; recebeu %v", tc.want, err)
				}
				return
			}
			if err != nil || e.Validate() != nil || e.Amount() != tc.amount || e.BalanceBefore() != tc.before || e.BalanceAfter() != tc.after {
				t.Fatalf("Lançamento válido foi alterado ou rejeitado: %v", err)
			}
		})
	}
	for _, id := range []string{"", " \t"} {
		if _, err := New(id, "wallet-1", "transaction-1", Credit, ten, zero, ten, now); !errors.Is(err, ErrInvalidEntry) {
			t.Fatalf("Identidade vazia deve ser rejeitada: %v", err)
		}
	}
	if _, err := New("entry-1", "wallet-1", "transaction-1", Credit, ten, zero, ten, time.Time{}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("Instante vazio deve ser rejeitado: %v", err)
	}
}
