package wallet

import (
	"errors"
	"math"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

func TestRehydratePreservesStoredState(t *testing.T) {
	createdAt := time.Date(2026, time.October, 17, 10, 30, 0, 123000000, time.FixedZone("BRT", -3*60*60))
	updatedAt := createdAt.Add(24 * time.Hour)
	cases := []struct {
		name      string
		amount    string
		currency  string
		version   int64
		updatedAt time.Time
	}{
		{name: "carteira movimentada", amount: "75.00", currency: "BRL", version: 8, updatedAt: updatedAt},
		{name: "carteira na criação", amount: "0.00", currency: "USD", version: 1, updatedAt: createdAt},
		{name: "limites numéricos", amount: "92233720368547758.07", currency: "BRL", version: math.MaxInt64, updatedAt: updatedAt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			balance, err := money.New(tc.amount, tc.currency)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Rehydrate("wallet-1", "player-1", balance, tc.version, createdAt, tc.updatedAt)
			if err != nil {
				t.Fatalf("Erro inesperado na reidratação: %v", err)
			}
			if got.ID() != "wallet-1" || got.PlayerID() != "player-1" {
				t.Fatal("Reidratação alterou as identidades")
			}
			if got.Balance() != balance || got.Version() != tc.version {
				t.Fatal("Reidratação alterou o saldo, a moeda ou a versão persistidos")
			}
			if !got.CreatedAt().Equal(createdAt) || !got.UpdatedAt().Equal(tc.updatedAt) {
				t.Fatal("Reidratação alterou os instantes persistidos")
			}
			if got.CreatedAt().Location() != time.UTC || got.UpdatedAt().Location() != time.UTC {
				t.Fatal("Instantes devem ser representados em UTC")
			}

			reloaded, err := Rehydrate(got.ID(), got.PlayerID(), got.Balance(), got.Version(), got.CreatedAt(), got.UpdatedAt())
			if err != nil || reloaded != got {
				t.Fatalf("Reidratar novamente deve preservar o mesmo estado: %v", err)
			}
		})
	}
}

func TestRehydrateRejectsInvalidState(t *testing.T) {
	createdAt := time.Date(2026, time.October, 17, 13, 30, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	balance, err := money.New("75.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	negative, err := balance.Negate()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		id        string
		playerID  string
		balance   money.Money
		version   int64
		createdAt time.Time
		updatedAt time.Time
		want      error
	}{
		{name: "id vazio", playerID: "player-1", balance: balance, version: 8, createdAt: createdAt, updatedAt: updatedAt, want: ErrInvalidID},
		{name: "id com espaços", id: " \t", playerID: "player-1", balance: balance, version: 8, createdAt: createdAt, updatedAt: updatedAt, want: ErrInvalidID},
		{name: "jogador vazio", id: "wallet-1", balance: balance, version: 8, createdAt: createdAt, updatedAt: updatedAt, want: ErrInvalidPlayerID},
		{name: "jogador com espaços", id: "wallet-1", playerID: " \t", balance: balance, version: 8, createdAt: createdAt, updatedAt: updatedAt, want: ErrInvalidPlayerID},
		{name: "saldo não inicializado", id: "wallet-1", playerID: "player-1", balance: money.Money{}, version: 8, createdAt: createdAt, updatedAt: updatedAt, want: money.ErrUninitialized},
		{name: "saldo negativo", id: "wallet-1", playerID: "player-1", balance: negative, version: 8, createdAt: createdAt, updatedAt: updatedAt, want: ErrNegativeBalance},
		{name: "versão zero", id: "wallet-1", playerID: "player-1", balance: balance, version: 0, createdAt: createdAt, updatedAt: updatedAt, want: ErrInvalidVersion},
		{name: "versão negativa", id: "wallet-1", playerID: "player-1", balance: balance, version: -1, createdAt: createdAt, updatedAt: updatedAt, want: ErrInvalidVersion},
		{name: "criação ausente", id: "wallet-1", playerID: "player-1", balance: balance, version: 8, updatedAt: updatedAt, want: ErrInvalidTime},
		{name: "atualização ausente", id: "wallet-1", playerID: "player-1", balance: balance, version: 8, createdAt: createdAt, want: ErrInvalidTime},
		{name: "atualização anterior à criação", id: "wallet-1", playerID: "player-1", balance: balance, version: 8, createdAt: createdAt, updatedAt: createdAt.Add(-time.Second), want: ErrInvalidTime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Rehydrate(tc.id, tc.playerID, tc.balance, tc.version, tc.createdAt, tc.updatedAt)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Esperava %v; recebeu %v", tc.want, err)
			}
			if got != (Wallet{}) {
				t.Fatal("Estado inválido deve retornar carteira vazia junto com o erro")
			}
		})
	}
}
