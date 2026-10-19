package wallet

import (
	"errors"
	"math"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

func TestCreditInvariantsAndOverflow(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		balance, amount, currency string
		version                   int64
		instant                   time.Time
		want                      error
	}{
		{"100.00", "25.00", "BRL", 1, at, nil},
		{"92233720368547758.06", "0.01", "BRL", 1, at, nil},
		{"92233720368547758.07", "0.01", "BRL", 1, at, money.ErrOverflow},
		{"0.00", "0.00", "BRL", 1, at, ErrInvalidMovement},
		{"1.00", "1.00", "USD", 1, at, money.ErrCurrencyMismatch},
		{"1.00", "1.00", "BRL", 1, time.Time{}, ErrInvalidTime},
		{"1.00", "1.00", "BRL", 1, at.Add(-time.Second), ErrInvalidTime},
		{"1.00", "1.00", "BRL", math.MaxInt64, at, ErrInvalidVersion},
	} {
		balance, _ := money.New(tc.balance, "BRL")
		amount, _ := money.New(tc.amount, tc.currency)
		w, _ := Rehydrate("w", "p", balance, tc.version, at, at)
		moved, err := w.Credit(amount, tc.instant)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%+v: %v", tc, err)
		}
		if tc.want == nil && (moved.Balance().MinorUnits() != balance.MinorUnits()+amount.MinorUnits() || moved.Version() != 2 || !moved.UpdatedAt().Equal(tc.instant)) {
			t.Fatal("Crédito incorreto")
		}
		if w.Balance() != balance || w.Version() != tc.version {
			t.Fatal("Crédito alterou operando")
		}
	}
	amount, _ := money.New("1.00", "BRL")
	w, _ := NewWallet("w", "p", amount, at)
	if _, err := (Wallet{}).Credit(amount, at); !errors.Is(err, ErrInvalidID) {
		t.Fatal(err)
	}
	if _, err := w.Credit(money.Money{}, at); !errors.Is(err, money.ErrUninitialized) {
		t.Fatal(err)
	}
}
