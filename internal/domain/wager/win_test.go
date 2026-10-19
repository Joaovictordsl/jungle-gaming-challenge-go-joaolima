package wager

import (
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func winFixture(t *testing.T, balance string, withReference bool) (Win, wallet.Wallet, *Reference) {
	t.Helper()
	base, _ := betFixture(t, "25.00")
	r := WinRequest{BetRequest: base.Request()}
	r.ExternalTransactionID = "win-1"
	var ref *Reference
	if withReference {
		r.ReferenceExternalTransactionID = "bet-1"
		referenceRequest := base.Request()
		referenceRequest.ExternalTransactionID = "bet-1"
		referenceRequest.Money, _ = money.New("80.00", "BRL")
		ref = &Reference{ID: "reference-id", Kind: BetKind, Status: Processed, Request: referenceRequest}
	}
	value, _ := money.New(balance, "BRL")
	current, _ := wallet.NewWallet(r.WalletID, r.PlayerID, value, base.CreatedAt())
	win, err := NewWin("win", r, base.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	return win, current, ref
}

func TestWinCreditRehydrationAndTerminalState(t *testing.T) {
	for _, withReference := range []bool{false, true} {
		pending, current, ref := winFixture(t, "100.00", withReference)
		completed, moved, err := pending.Apply(current, ref, pending.CreatedAt())
		if err != nil || completed.Status() != Processed || moved.Balance().MinorUnits() != 12500 || moved.Version() != 2 || current.Balance().MinorUnits() != 10000 || pending.Status() != Pending {
			t.Fatal("Crédito inválido", err)
		}
		if withReference && completed.ReferenceID() != ref.ID {
			t.Fatal("Referência interna não preservada")
		}
		restored, err := RehydrateWin(completed.ID(), completed.Request(), completed.Status(), completed.FailureCode(), completed.ReferenceID(), completed.BalanceBefore(), completed.Balance(), completed.WalletVersion(), completed.CreatedAt(), completed.UpdatedAt())
		if err != nil || restored != completed {
			t.Fatal("Reidratação reaplicou ou alterou crédito", err)
		}
		if _, _, err := restored.Apply(moved, ref, restored.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
			t.Fatal("Terminal aceitou nova transição", err)
		}
	}
}

func TestWinReferenceRules(t *testing.T) {
	pending, current, ref := winFixture(t, "100.00", true)
	waiting, unchanged, err := pending.Apply(current, nil, pending.CreatedAt())
	if err != nil || waiting.Status() != PendingReference || unchanged != current || waiting.ReferenceID() != "" {
		t.Fatal("Referência ausente não criou espera", err)
	}
	processed, _, err := waiting.Apply(current, ref, waiting.UpdatedAt().Add(time.Second))
	if err != nil || processed.Status() != Processed {
		t.Fatal("Referência posterior não liberou crédito", err)
	}
	expired, err := waiting.ExpireReference(current, waiting.UpdatedAt().Add(time.Minute))
	if err != nil || expired.Status() != Rejected || expired.FailureCode() != ReferenceNotFound {
		t.Fatal("Expiração não auditável", err)
	}
	for _, edit := range []func(*Reference){
		func(r *Reference) { r.Request.ProviderID = "other" }, func(r *Reference) { r.Request.PlayerID = "other" },
		func(r *Reference) { r.Request.WalletID = "other" }, func(r *Reference) { r.Request.RoundID = "other" },
		func(r *Reference) { r.Request.GameID = "other" }, func(r *Reference) { r.Kind = WinKind },
		func(r *Reference) { r.Kind = LossKind; r.Request.Money, _ = money.Zero("BRL") },
		func(r *Reference) { r.Request.Money, _ = money.New("1.00", "USD") },
	} {
		changed := *ref
		edit(&changed)
		rejected, w, err := pending.Apply(current, &changed, pending.CreatedAt())
		if err != nil || rejected.Status() != Rejected || rejected.FailureCode() != ReferenceMismatch || w != current {
			t.Fatal("Referência incompatível creditou", err)
		}
	}
	changed := *ref
	changed.Status = Rejected
	rejected, w, err := pending.Apply(current, &changed, pending.CreatedAt())
	if err != nil || rejected.FailureCode() != ReferenceRejected || w != current {
		t.Fatal("Referência rejeitada creditou", err)
	}
	changed.Status = Pending
	waiting, w, err = pending.Apply(current, &changed, pending.CreatedAt())
	if err != nil || waiting.Status() != PendingReference || w != current {
		t.Fatal("Referência pendente creditou", err)
	}
}

func TestWinOverflowAndInvalidInputs(t *testing.T) {
	pending, current, _ := winFixture(t, "92233720368547758.07", false)
	rejected, w, err := pending.Apply(current, nil, pending.CreatedAt())
	if err != nil || rejected.Status() != Rejected || rejected.FailureCode() != BalanceOverflow || w != current || rejected.WalletVersion() != current.Version() {
		t.Fatal("Overflow não preservou saldo", err)
	}
	if _, _, err := rejected.Apply(current, nil, rejected.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
		t.Fatal(err)
	}
	r := pending.Request()
	r.Money, _ = money.New("0.00", "BRL")
	if _, err := NewWin("invalid", r, pending.CreatedAt()); err == nil {
		t.Fatal("Zero aceito")
	}
	r = pending.Request()
	r.ReferenceExternalTransactionID = r.ExternalTransactionID
	if _, err := NewWin("invalid", r, pending.CreatedAt()); err == nil {
		t.Fatal("Autorreferência aceita")
	}
	if _, _, err := (Win{}).Apply(current, nil, pending.CreatedAt()); err == nil {
		t.Fatal("Zero value aceito")
	}
	if _, _, err := pending.Apply(current, nil, time.Time{}); err == nil {
		t.Fatal("Instante zero aceito")
	}
	r = pending.Request()
	r.PlayerID = "other"
	changed, _ := NewWin("other", r, pending.CreatedAt())
	if _, _, err := changed.Apply(current, nil, pending.CreatedAt()); !errors.Is(err, ErrWalletMismatch) {
		t.Fatal(err)
	}
}

func TestWinCanonicalHashIncludesKindAndReference(t *testing.T) {
	pending, _, _ := winFixture(t, "100.00", false)
	r := pending.Request()
	first, _ := r.PayloadHash()
	r.Money, _ = money.New("00025.00", "BRL")
	r.IdempotencyKey = "another-key"
	equivalent, _ := r.PayloadHash()
	if first != equivalent {
		t.Fatal("Dinheiro equivalente mudou hash")
	}
	betHash, _ := r.BetRequest.PayloadHash()
	if first == betHash {
		t.Fatal("BET e WIN colidiram")
	}
	r.ReferenceExternalTransactionID = "bet-1"
	withReference, _ := r.PayloadHash()
	if first == withReference {
		t.Fatal("Referência não participa do hash")
	}
	r.ReferenceExternalTransactionID = "bet-2"
	otherReference, _ := r.PayloadHash()
	if withReference == otherReference {
		t.Fatal("Mudança de referência não participa do hash")
	}
}
