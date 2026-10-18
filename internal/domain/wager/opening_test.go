package wager

import (
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func TestOpeningLifecycle(t *testing.T) {
	balance, _ := money.New("100.00", "BRL")
	w, err := wallet.NewWallet("wallet-1", "player-1", balance, time.Date(2026, 10, 18, 13, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := NewOpening(w)
	if err != nil || pending.Status() != Pending || pending.Wallet() != w {
		t.Fatalf("Abertura deve começar pendente com a carteira original: %v", err)
	}
	processed, err := pending.Process()
	if err != nil || processed.Status() != Processed || pending.Status() != Pending || processed.ID() != pending.ID() {
		t.Fatalf("Processamento deve preservar identidade e o valor original: %v", err)
	}
	if _, err := processed.Process(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Estado terminal não deve aceitar novo processamento: %v", err)
	}
}

func TestOpeningRejectsZeroAndNonInitialWallet(t *testing.T) {
	zero, _ := money.Zero("BRL")
	now := time.Date(2026, 10, 18, 13, 30, 0, 0, time.UTC)
	w, _ := wallet.NewWallet("wallet-1", "player-1", zero, now)
	if _, err := NewOpening(w); !errors.Is(err, ErrInvalidOpening) {
		t.Fatalf("Saldo zero não deve produzir OPENING: %v", err)
	}
	balance, _ := money.New("10.00", "BRL")
	for _, version := range []int64{1, 2} {
		updated := now
		if version == 1 {
			updated = now.Add(time.Second)
		}
		w, _ := wallet.Rehydrate("wallet-1", "player-1", balance, version, now, updated)
		if _, err := NewOpening(w); !errors.Is(err, wallet.ErrNotInitialState) {
			t.Fatalf("Carteira fora do estado inicial não pode abrir novamente: %v", err)
		}
	}
	if _, err := (Opening{}).Process(); err == nil {
		t.Fatal("Abertura não inicializada deve ser rejeitada")
	}
}
