package wager

import (
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func betFixture(t *testing.T, amount string) (Bet, wallet.Wallet) {
	t.Helper()
	value, _ := money.New(amount, "BRL")
	balance, _ := money.New("100.00", "BRL")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w, _ := wallet.NewWallet("wallet", "player", balance, at)
	b, err := NewBet("bet", BetRequest{ProviderID: "provider", ExternalTransactionID: "external", IdempotencyKey: "key", WalletID: w.ID(), PlayerID: w.PlayerID(), RoundID: "round", GameID: "game", Money: value}, at)
	if err != nil {
		t.Fatal(err)
	}
	return b, w
}

func TestBetTransitionsAndRehydration(t *testing.T) {
	for _, tc := range []struct {
		amount, status string
		cents, version int64
	}{
		{"80.00", Processed, 2000, 2}, {"100.00", Processed, 0, 2}, {"100.01", Rejected, 10000, 1},
	} {
		pending, w := betFixture(t, tc.amount)
		result, moved, err := pending.Apply(w, w.UpdatedAt())
		if err != nil || result.Status() != tc.status || moved.Balance().MinorUnits() != tc.cents || moved.Version() != tc.version || pending.Status() != Pending || w.Balance().MinorUnits() != 10000 {
			t.Fatalf("Transição inválida: %v", err)
		}
		if tc.status == Rejected && result.FailureCode() != InsufficientFunds {
			t.Fatal("Código de rejeição instável")
		}
		restored, err := RehydrateBet(result.ID(), result.Request(), result.Status(), result.FailureCode(), result.BalanceBefore(), result.Balance(), result.WalletVersion(), result.CreatedAt(), result.UpdatedAt())
		if err != nil || restored != result {
			t.Fatalf("Reidratação mudou estado: %v", err)
		}
		if _, _, err := restored.Apply(moved, result.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Terminal reaplicado: %v", err)
		}
	}
}

func TestBetRejectsInvalidInputsAndWalletMismatch(t *testing.T) {
	b, w := betFixture(t, "1.00")
	for _, edit := range []func(*BetRequest){
		func(r *BetRequest) { r.ProviderID = "" }, func(r *BetRequest) { r.ExternalTransactionID = " " },
		func(r *BetRequest) { r.IdempotencyKey = "" }, func(r *BetRequest) { r.RoundID = "" }, func(r *BetRequest) { r.GameID = "" },
		func(r *BetRequest) { r.WalletID = "" }, func(r *BetRequest) { r.PlayerID = "" },
		func(r *BetRequest) { r.Money, _ = money.New("0.00", "BRL") }, func(r *BetRequest) { r.Money = money.Money{} },
	} {
		r := b.Request()
		edit(&r)
		if _, err := NewBet("id", r, w.CreatedAt()); err == nil {
			t.Fatal("Aceitou entrada inválida")
		}
	}
	for _, edit := range []func(*BetRequest){func(r *BetRequest) { r.WalletID = "other" }, func(r *BetRequest) { r.PlayerID = "other" }, func(r *BetRequest) { r.Money, _ = money.New("1.00", "USD") }} {
		r := b.Request()
		edit(&r)
		candidate, _ := NewBet("id", r, w.CreatedAt())
		if _, _, err := candidate.Apply(w, w.CreatedAt()); !errors.Is(err, ErrWalletMismatch) {
			t.Fatal(err)
		}
	}
	if _, _, err := (Bet{}).Apply(w, w.CreatedAt()); err == nil {
		t.Fatal("Aceitou zero value")
	}
	if _, _, err := b.Apply(w, time.Time{}); err == nil {
		t.Fatal("Aceitou instante zero")
	}
	result, _, _ := b.Apply(w, w.CreatedAt())
	result.balance = w.Balance()
	if err := result.Validate(); err == nil {
		t.Fatal("Aceitou resultado incoerente")
	}
}

func TestBetCanonicalHash(t *testing.T) {
	b, _ := betFixture(t, "01.00")
	r := b.Request()
	h1, _ := r.PayloadHash()
	r.Money, _ = money.New("0001.00", "BRL")
	r.IdempotencyKey = "other"
	h2, _ := r.PayloadHash()
	if h1 != h2 || len(h1) != 64 {
		t.Fatal("Hash deve normalizar dinheiro e excluir a chave")
	}
	for _, edit := range []func(*BetRequest){
		func(r *BetRequest) { r.ProviderID = "other" }, func(r *BetRequest) { r.ExternalTransactionID = "other" }, func(r *BetRequest) { r.WalletID = "other" },
		func(r *BetRequest) { r.PlayerID = "other" }, func(r *BetRequest) { r.RoundID = "other" }, func(r *BetRequest) { r.GameID = "other" },
		func(r *BetRequest) { r.Money, _ = money.New("2.00", "BRL") }, func(r *BetRequest) { r.Money, _ = money.New("1.00", "USD") },
	} {
		changed := r
		edit(&changed)
		hash, _ := changed.PayloadHash()
		if hash == h1 {
			t.Fatal("Campo financeiro não participa do hash")
		}
	}
}
