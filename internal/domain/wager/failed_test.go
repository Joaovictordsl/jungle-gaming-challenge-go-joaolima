package wager

import (
	"errors"
	"testing"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

func TestFailedBetIsTerminalAndRequiresUnchangedSnapshot(t *testing.T) {
	pending, current := betFixture(t, "80.00")
	failed, err := pending.FailInfrastructure(current, current.UpdatedAt())
	if err != nil || failed.Status() != Failed || failed.FailureCode() != InfrastructureRetriesExhausted || failed.Balance() != current.Balance() || failed.BalanceBefore() != current.Balance() || failed.WalletVersion() != current.Version() {
		t.Fatal("snapshot da falha inválido", err)
	}
	restored, err := RehydrateBet(failed.ID(), failed.Request(), Failed, failed.FailureCode(), failed.BalanceBefore(), failed.Balance(), failed.WalletVersion(), failed.CreatedAt(), failed.UpdatedAt())
	if err != nil || restored != failed {
		t.Fatal("reidratação alterou falha", err)
	}
	if _, _, err := failed.Apply(current, failed.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
		t.Fatal("falha reaplicada", err)
	}
	if _, err := failed.FailInfrastructure(current, failed.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
		t.Fatal("falha alterada", err)
	}
	for _, edit := range []func(*Bet){
		func(b *Bet) { b.failureCode = InsufficientFunds },
		func(b *Bet) { b.balance, _ = money.New("20.00", "BRL") },
		func(b *Bet) { b.walletVersion = 0 },
	} {
		invalid := failed
		edit(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("snapshot FAILED inválido aceito")
		}
	}
}

func TestFailedReferencedTransactionsAndReferences(t *testing.T) {
	b, current := betFixture(t, "80.00")
	r := WinRequest{BetRequest: b.Request(), ReferenceExternalTransactionID: "original"}
	for _, kind := range []string{WinKind, RefundKind, RollbackKind} {
		t.Run(kind, func(t *testing.T) {
			var pending ReferencedTransaction
			var err error
			switch kind {
			case WinKind:
				pending, err = NewWin("id", r, current.CreatedAt())
			case RefundKind:
				pending, err = NewRefund("id", RefundRequest{WinRequest: r}, current.CreatedAt())
			case RollbackKind:
				pending, err = NewRollback("id", RollbackRequest{WinRequest: r}, current.CreatedAt())
			}
			if err != nil {
				t.Fatal(err)
			}
			failed, err := pending.FailInfrastructure(current, current.UpdatedAt())
			if err != nil || failed.Status() != Failed || failed.BalanceBefore() != failed.Balance() || failed.WalletVersion() != current.Version() || failed.FailureCode() != InfrastructureRetriesExhausted {
				t.Fatal(err)
			}
			if _, _, err := failed.Apply(current, nil, failed.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
				t.Fatal("terminal reaplicado", err)
			}
			if _, err := failed.FailInfrastructure(current, failed.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
				t.Fatal("terminal alterado", err)
			}
			failed.referenceID = "invalid"
			if failed.Validate() == nil {
				t.Fatal("FAILED aceitou referência resolvida")
			}
		})
	}
	ref := Reference{ID: "failed-bet", Kind: BetKind, Status: Failed, Request: b.Request()}
	ref.Request.ExternalTransactionID = "original"
	pending, _ := NewWin("win", r, current.CreatedAt())
	result, moved, err := pending.Apply(current, &ref, current.UpdatedAt())
	if err != nil || result.Status() != Rejected || result.FailureCode() != ReferenceRejected || moved != current {
		t.Fatal("referência FAILED não foi rejeitada", err)
	}
}

func TestFailedLossIsTerminal(t *testing.T) {
	b, current := betFixture(t, "1.00")
	r := LossRequest{BetRequest: b.Request()}
	r.Money, _ = money.New("0.00", "BRL")
	pending, _ := NewLoss("loss", r, current.CreatedAt())
	failed, err := pending.FailInfrastructure(current, current.UpdatedAt())
	if err != nil || failed.Status() != Failed || failed.FailureCode() != InfrastructureRetriesExhausted || failed.Balance() != current.Balance() {
		t.Fatal(err)
	}
	restored, err := RehydrateFailedLoss(failed.ID(), r, failed.FailureCode(), failed.Balance(), failed.WalletVersion(), failed.CreatedAt(), failed.UpdatedAt())
	if err != nil || restored != failed {
		t.Fatal(err)
	}
	if _, err := failed.Process(current, failed.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
		t.Fatal("LOSS reaplicado", err)
	}
	if _, err := failed.FailInfrastructure(current, failed.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
		t.Fatal("LOSS alterado", err)
	}
}
