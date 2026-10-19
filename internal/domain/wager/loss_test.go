package wager

import (
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func lossFixture(t *testing.T) (Loss, wallet.Wallet) {
	t.Helper()
	base, current := betFixture(t, "1.00")
	r := base.Request()
	r.Money, _ = money.New("0.00", "BRL")
	loss, err := NewLoss("loss", LossRequest{BetRequest: r}, base.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	return loss, current
}

func TestLossProcessingPreservesWalletAndRehydration(t *testing.T) {
	pending, current := lossFixture(t)
	original := current
	completed, err := pending.Process(current, pending.CreatedAt().Add(time.Second))
	if err != nil || completed.Status() != Processed || completed.Balance() != current.Balance() || completed.WalletVersion() != current.Version() || current != original || pending.Status() != Pending {
		t.Fatal("LOSS alterou carteira ou estado de origem", err)
	}
	restored, err := RehydrateLoss(completed.ID(), completed.Request(), completed.Balance(), completed.WalletVersion(), completed.CreatedAt(), completed.UpdatedAt())
	if err != nil || restored != completed {
		t.Fatal("Reidratação alterou resultado", err)
	}
	if _, err := restored.Process(current, completed.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
		t.Fatal("Terminal aceitou nova transição", err)
	}
	zero, _ := money.Zero("BRL")
	empty, _ := wallet.NewWallet(current.ID(), current.PlayerID(), zero, current.CreatedAt())
	if result, err := pending.Process(empty, pending.CreatedAt()); err != nil || result.Balance().MinorUnits() != 0 || result.WalletVersion() != 1 {
		t.Fatal("LOSS rejeitou carteira sem saldo", err)
	}
}

func TestLossRejectsInvalidValuesAndWallets(t *testing.T) {
	pending, current := lossFixture(t)
	for _, edit := range []func(*LossRequest){
		func(r *LossRequest) { r.Money, _ = money.New("0.01", "BRL") }, func(r *LossRequest) { r.Money, _ = money.FromMinorUnits(-1, "BRL") },
		func(r *LossRequest) { r.Money = money.Money{} }, func(r *LossRequest) { r.ProviderID = "" }, func(r *LossRequest) { r.ExternalTransactionID = "" },
		func(r *LossRequest) { r.IdempotencyKey = " " }, func(r *LossRequest) { r.GameID = "" }, func(r *LossRequest) { r.RoundID = "" },
	} {
		r := pending.Request()
		edit(&r)
		if _, err := NewLoss("invalid", r, pending.CreatedAt()); err == nil {
			t.Fatal("LOSS aceitou entrada inválida")
		}
	}
	for _, edit := range []func(*LossRequest){func(r *LossRequest) { r.PlayerID = "other" }, func(r *LossRequest) { r.WalletID = "other" }, func(r *LossRequest) { r.Money, _ = money.Zero("USD") }} {
		r := pending.Request()
		edit(&r)
		candidate, _ := NewLoss("mismatch", r, pending.CreatedAt())
		if _, err := candidate.Process(current, pending.CreatedAt()); !errors.Is(err, ErrWalletMismatch) {
			t.Fatal("LOSS aceitou carteira incompatível", err)
		}
	}
	if _, err := (Loss{}).Process(current, pending.CreatedAt()); err == nil {
		t.Fatal("LOSS aceitou zero value")
	}
	if _, err := pending.Process(current, time.Time{}); err == nil {
		t.Fatal("LOSS aceitou instante zero")
	}
	if _, err := pending.Process(wallet.Wallet{}, pending.CreatedAt()); err == nil {
		t.Fatal("LOSS aceitou carteira inválida")
	}
	if _, err := RehydrateLoss(pending.ID(), pending.Request(), current.Balance(), 0, pending.CreatedAt(), pending.UpdatedAt()); err == nil {
		t.Fatal("Reidratação aceitou versão inválida")
	}
}

func TestLossCanonicalHash(t *testing.T) {
	pending, _ := lossFixture(t)
	r := pending.Request()
	original, _ := r.PayloadHash()
	r.Money, _ = money.New("0000.00", "BRL")
	r.IdempotencyKey = "other-key"
	normalized, _ := r.PayloadHash()
	if normalized != original || len(original) != 64 {
		t.Fatal("Normalização ou exclusão da chave incorreta")
	}
	for _, edit := range []func(*LossRequest){
		func(r *LossRequest) { r.ProviderID = "other" }, func(r *LossRequest) { r.ExternalTransactionID = "other" }, func(r *LossRequest) { r.WalletID = "other" },
		func(r *LossRequest) { r.PlayerID = "other" }, func(r *LossRequest) { r.GameID = "other" }, func(r *LossRequest) { r.RoundID = "other" }, func(r *LossRequest) { r.Money, _ = money.Zero("USD") },
	} {
		changed := r
		edit(&changed)
		hash, err := changed.PayloadHash()
		if err != nil || hash == original {
			t.Fatal("Hash não representa todos os campos", err)
		}
	}
}
