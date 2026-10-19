package wallet

import (
	"errors"
	"math"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

func TestDebitPreservesWalletInvariants(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	balance, _ := money.New("100.00", "BRL")
	original, _ := NewWallet("w", "p", balance, at)
	for _, tc := range []struct {
		amount, currency string
		version          int64
		at               time.Time
		want             error
	}{
		{"80.00", "BRL", 1, at.Add(time.Second), nil},
		{"100.00", "BRL", 1, at, nil},
		{"100.01", "BRL", 1, at, ErrInsufficientFunds},
		{"1.00", "USD", 1, at, money.ErrCurrencyMismatch},
		{"0.00", "BRL", 1, at, ErrInvalidMovement},
		{"1.00", "BRL", 1, at.Add(-time.Second), ErrInvalidTime},
		{"1.00", "BRL", math.MaxInt64, at, ErrInvalidVersion},
	} {
		amount, _ := money.New(tc.amount, tc.currency)
		w := original
		w.version = tc.version
		moved, err := w.Debit(amount, tc.at)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%+v: %v", tc, err)
		}
		if tc.want == nil && (moved.Balance().MinorUnits() != balance.MinorUnits()-amount.MinorUnits() || moved.Version() != 2 || !moved.UpdatedAt().Equal(tc.at)) {
			t.Fatal("Débito não preservou saldo, versão e instante")
		}
		if w.Balance() != balance || w.Version() != tc.version {
			t.Fatal("Débito alterou o operando")
		}
	}
	if _, err := (Wallet{}).Debit(balance, at); !errors.Is(err, ErrInvalidID) {
		t.Fatal(err)
	}
	if _, err := original.Debit(money.Money{}, at); !errors.Is(err, money.ErrUninitialized) {
		t.Fatal(err)
	}
}
