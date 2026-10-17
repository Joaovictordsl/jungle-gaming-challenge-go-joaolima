package wallet

import (
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

func TestNewWalletInitialState(t *testing.T) {
	createdAt := time.Date(2026, time.October, 17, 10, 30, 0, 123000000, time.FixedZone("BRT", -3*60*60))
	cases := []struct {
		name     string
		amount   string
		currency string
	}{
		{name: "saldo positivo", amount: "100.00", currency: "BRL"},
		{name: "saldo zero", amount: "0.00", currency: "BRL"},
		{name: "outra moeda", amount: "25.05", currency: "USD"},
		{name: "saldo máximo", amount: "92233720368547758.07", currency: "BRL"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			balance, err := money.New(tc.amount, tc.currency)
			if err != nil {
				t.Fatalf("Erro ao preparar saldo: %v", err)
			}

			got, err := NewWallet("wallet-1", "player-1", balance, createdAt)
			if err != nil {
				t.Fatalf("Erro inesperado na criação: %v", err)
			}
			if got.id != "wallet-1" || got.playerID != "player-1" {
				t.Fatalf("Identidades incorretas: carteira=%q jogador=%q", got.id, got.playerID)
			}
			if got.balance != balance {
				t.Fatal("Saldo inicial ou moeda foram alterados")
			}
			if got.version != 1 {
				t.Fatalf("Esperava versão inicial 1; recebeu %d", got.version)
			}
			if !got.createdAt.Equal(createdAt) || !got.updatedAt.Equal(createdAt) {
				t.Fatal("Criação e atualização devem preservar o instante informado")
			}
			if got.createdAt.Location() != time.UTC || got.updatedAt.Location() != time.UTC {
				t.Fatal("Criação e atualização devem estar em UTC")
			}
		})
	}
}

func TestNewWalletRejectsInvalidInputs(t *testing.T) {
	createdAt := time.Date(2026, time.October, 17, 13, 30, 0, 0, time.UTC)
	balance, err := money.New("100.00", "BRL")
	if err != nil {
		t.Fatalf("Erro ao preparar saldo: %v", err)
	}
	negative, err := balance.Negate()
	if err != nil {
		t.Fatalf("Erro ao preparar saldo negativo: %v", err)
	}

	cases := []struct {
		name      string
		id        string
		playerID  string
		balance   money.Money
		createdAt time.Time
		want      error
	}{
		{name: "id vazio", playerID: "player-1", balance: balance, createdAt: createdAt, want: ErrInvalidID},
		{name: "id com espaços", id: " \t\n", playerID: "player-1", balance: balance, createdAt: createdAt, want: ErrInvalidID},
		{name: "jogador vazio", id: "wallet-1", balance: balance, createdAt: createdAt, want: ErrInvalidPlayerID},
		{name: "jogador com espaços", id: "wallet-1", playerID: " \t\n", balance: balance, createdAt: createdAt, want: ErrInvalidPlayerID},
		{name: "saldo não inicializado", id: "wallet-1", playerID: "player-1", balance: money.Money{}, createdAt: createdAt, want: money.ErrUninitialized},
		{name: "saldo negativo", id: "wallet-1", playerID: "player-1", balance: negative, createdAt: createdAt, want: ErrNegativeBalance},
		{name: "instante vazio", id: "wallet-1", playerID: "player-1", balance: balance, want: ErrInvalidTime},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewWallet(tc.id, tc.playerID, tc.balance, tc.createdAt)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Esperava erro %v; recebeu %v", tc.want, err)
			}
			if got != (Wallet{}) {
				t.Fatal("Entrada inválida deve retornar uma carteira vazia junto com o erro")
			}
		})
	}
}
