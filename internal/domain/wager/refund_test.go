package wager

import (
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func refundFixture(t *testing.T) (Refund, wallet.Wallet, *Reference) {
	t.Helper()
	win, _, ref := winFixture(t, "100.00", true)
	r := win.Request()
	r.ExternalTransactionID = "refund-1"
	r.IdempotencyKey = "refund-key"
	r.Money, _ = money.New("80.00", "BRL")
	balance, _ := money.New("20.00", "BRL")
	current, _ := wallet.Rehydrate(r.WalletID, r.PlayerID, balance, 2, win.CreatedAt(), win.CreatedAt().Add(time.Second))
	refund, err := NewRefund("refund", RefundRequest{WinRequest: r}, win.CreatedAt().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return refund, current, ref
}

func TestRefundFullCreditAndRehydration(t *testing.T) {
	pending, current, ref := refundFixture(t)
	completed, moved, err := pending.Apply(current, ref, pending.CreatedAt())
	if err != nil || completed.Kind() != RefundKind || completed.Status() != Processed || moved.Balance().MinorUnits() != 10000 || moved.Version() != 3 || pending.Status() != Pending || current.Balance().MinorUnits() != 2000 {
		t.Fatal("REFUND não devolveu integralmente", err)
	}
	restored, err := RehydrateRefund(completed.ID(), RefundRequest{WinRequest: completed.Request()}, completed.Status(), completed.FailureCode(), completed.ReferenceID(), completed.BalanceBefore(), completed.Balance(), completed.WalletVersion(), completed.CreatedAt(), completed.UpdatedAt())
	if err != nil || restored != completed {
		t.Fatal("Reidratação alterou devolução", err)
	}
	if _, _, err := restored.Apply(moved, ref, restored.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
		t.Fatal("Terminal foi reaplicado", err)
	}
}

func TestRefundReferenceAndDuplicateRules(t *testing.T) {
	pending, current, reference := refundFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*Reference)
		code string
	}{
		{"parcial", func(r *Reference) { r.Request.Money, _ = money.New("100.00", "BRL") }, ReferenceAmountMismatch},
		{"devolvida", func(r *Reference) { r.Reversed = true }, AlreadyReversed},
		{"referência rejeitada", func(r *Reference) { r.Status = Rejected }, ReferenceRejected},
		{"rodada", func(r *Reference) { r.Request.RoundID = "other" }, ReferenceMismatch},
		{"jogador", func(r *Reference) { r.Request.PlayerID = "other" }, ReferenceMismatch},
		{"carteira", func(r *Reference) { r.Request.WalletID = "other" }, ReferenceMismatch},
		{"provedor", func(r *Reference) { r.Request.ProviderID = "other" }, ReferenceMismatch},
		{"tipo", func(r *Reference) { r.Kind = WinKind }, ReferenceMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := *reference
			tc.edit(&ref)
			rejected, unchanged, err := pending.Apply(current, &ref, pending.CreatedAt())
			if err != nil || rejected.Status() != Rejected || rejected.FailureCode() != tc.code || unchanged != current || rejected.WalletVersion() != current.Version() {
				t.Fatal("Rejeição inválida ou carteira alterada", err)
			}
		})
	}
	waiting, unchanged, err := pending.Apply(current, nil, pending.CreatedAt())
	if err != nil || waiting.Status() != PendingReference || unchanged != current {
		t.Fatal("Ausência de referência não persistiu espera", err)
	}
	completed, _, err := waiting.Apply(current, reference, waiting.UpdatedAt().Add(time.Second))
	if err != nil || completed.Status() != Processed {
		t.Fatal(err)
	}
	expired, err := waiting.ExpireReference(current, waiting.UpdatedAt().Add(time.Minute))
	if err != nil || expired.FailureCode() != ReferenceNotFound {
		t.Fatal(err)
	}
	maximum, _ := money.New("92233720368547758.07", "BRL")
	full, _ := wallet.Rehydrate(current.ID(), current.PlayerID(), maximum, current.Version(), current.CreatedAt(), current.UpdatedAt())
	rejected, _, err := pending.Apply(full, reference, pending.CreatedAt())
	if err != nil || rejected.FailureCode() != BalanceOverflow {
		t.Fatal("Overflow não rejeitado", err)
	}
}

func TestRefundInputAndHash(t *testing.T) {
	pending, _, _ := refundFixture(t)
	r := RefundRequest{WinRequest: pending.Request()}
	hash, _ := r.PayloadHash()
	same := r
	same.Money, _ = money.New("00080.00", "BRL")
	same.IdempotencyKey = "another-key"
	normalized, _ := same.PayloadHash()
	if hash != normalized {
		t.Fatal("Normalização mudou identidade")
	}
	winHash, _ := r.WinRequest.PayloadHash()
	if hash == winHash {
		t.Fatal("WIN e REFUND colidiram")
	}
	same.ReferenceExternalTransactionID = "another-bet"
	changed, _ := same.PayloadHash()
	if changed == hash {
		t.Fatal("Referência não participa do hash")
	}
	for _, edit := range []func(*RefundRequest){func(r *RefundRequest) { r.ReferenceExternalTransactionID = "" }, func(r *RefundRequest) { r.ReferenceExternalTransactionID = r.ExternalTransactionID }, func(r *RefundRequest) { r.Money, _ = money.Zero("BRL") }} {
		invalid := r
		edit(&invalid)
		if _, err := NewRefund("invalid", invalid, pending.CreatedAt()); err == nil {
			t.Fatal("Entrada inválida aceita")
		}
	}
}
