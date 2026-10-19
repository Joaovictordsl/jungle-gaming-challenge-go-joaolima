package wager

import (
	"errors"
	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"testing"
	"time"
)

func rollbackFixture(t *testing.T, kind, balance string) (Rollback, wallet.Wallet, *Reference) {
	t.Helper()
	refund, current, ref := refundFixture(t)
	r := refund.Request()
	r.ExternalTransactionID = "rollback-1"
	pending, err := NewRollback("rollback", RollbackRequest{WinRequest: r}, refund.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	amount, _ := money.New(balance, "BRL")
	current, err = wallet.Rehydrate(current.ID(), current.PlayerID(), amount, current.Version(), current.CreatedAt(), current.UpdatedAt())
	if err != nil {
		t.Fatal(err)
	}
	ref.Kind = kind
	return pending, current, ref
}
func TestRollbackInvertsOriginalMovement(t *testing.T) {
	for _, tc := range []struct {
		kind, before, direction string
		after                   int64
	}{{BetKind, "20.00", ledger.Credit, 10000}, {WinKind, "100.00", ledger.Debit, 2000}, {RefundKind, "100.00", ledger.Debit, 2000}} {
		t.Run(tc.kind, func(t *testing.T) {
			pending, current, ref := rollbackFixture(t, tc.kind, tc.before)
			completed, moved, err := pending.Apply(current, ref, pending.CreatedAt())
			if err != nil || completed.Status() != Processed || completed.Direction() != tc.direction || moved.Balance().MinorUnits() != tc.after || moved.Version() != 3 || current.Version() != 2 || pending.Status() != Pending {
				t.Fatal("Movimentação inversa inválida", err)
			}
			restored, err := RehydrateRollback(completed.ID(), RollbackRequest{WinRequest: completed.Request()}, completed.Status(), completed.FailureCode(), completed.ReferenceID(), completed.Direction(), completed.BalanceBefore(), completed.Balance(), completed.WalletVersion(), completed.CreatedAt(), completed.UpdatedAt())
			if err != nil || restored != completed {
				t.Fatal("Reidratação alterou estado", err)
			}
			if _, _, err := completed.Apply(moved, ref, completed.UpdatedAt()); !errors.Is(err, ErrInvalidState) {
				t.Fatal("Estorno terminal reaplicado", err)
			}
		})
	}
}
func TestRollbackInsufficientBalanceAndOverflow(t *testing.T) {
	for _, kind := range []string{WinKind, RefundKind} {
		pending, current, ref := rollbackFixture(t, kind, "79.99")
		rejected, unchanged, err := pending.Apply(current, ref, pending.CreatedAt())
		if err != nil || unchanged != current || rejected.FailureCode() != ReversalInsufficientFunds || rejected.FailureCode() == InsufficientFunds || rejected.Direction() != ledger.Debit {
			t.Fatal("Saldo insuficiente não auditado", err)
		}
	}
	pending, current, ref := rollbackFixture(t, BetKind, "92233720368547758.07")
	rejected, unchanged, err := pending.Apply(current, ref, pending.CreatedAt())
	if err != nil || rejected.FailureCode() != BalanceOverflow || unchanged != current {
		t.Fatal("Overflow creditado", err)
	}
	pending, current, ref = rollbackFixture(t, WinKind, "80.00")
	processed, moved, err := pending.Apply(current, ref, pending.CreatedAt())
	if err != nil || processed.Status() != Processed || moved.Balance().MinorUnits() != 0 {
		t.Fatal("Débito exato recusado", err)
	}
}
func TestRollbackReferenceRulesAndPending(t *testing.T) {
	pending, current, original := rollbackFixture(t, BetKind, "100.00")
	for _, tc := range []struct {
		name, code string
		edit       func(*Reference)
	}{
		{"parcial", ReferenceAmountMismatch, func(r *Reference) { r.Request.Money, _ = money.New("81.00", "BRL") }},
		{"duplicada", AlreadyReversed, func(r *Reference) { r.Reversed = true }},
		{"rejeitada", ReferenceRejected, func(r *Reference) { r.Status = Rejected }},
		{"rollback", ReferenceMismatch, func(r *Reference) { r.Kind = RollbackKind }},
		{"loss", ReferenceMismatch, func(r *Reference) { r.Kind = LossKind; r.Request.Money, _ = money.Zero("BRL") }},
		{"rodada", ReferenceMismatch, func(r *Reference) { r.Request.RoundID = "other" }},
		{"jogo", ReferenceMismatch, func(r *Reference) { r.Request.GameID = "other" }},
		{"provedor", ReferenceMismatch, func(r *Reference) { r.Request.ProviderID = "other" }},
		{"moeda", ReferenceMismatch, func(r *Reference) { r.Request.Money, _ = money.New("80.00", "USD") }},
		{"carteira", ReferenceMismatch, func(r *Reference) { r.Request.WalletID = "other" }},
		{"jogador", ReferenceMismatch, func(r *Reference) { r.Request.PlayerID = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := *original
			tc.edit(&ref)
			rejected, unchanged, err := pending.Apply(current, &ref, pending.CreatedAt())
			if err != nil || rejected.FailureCode() != tc.code || unchanged != current {
				t.Fatal("Rejeição inválida", err)
			}
		})
	}
	for _, ref := range []*Reference{nil, {ID: original.ID, Kind: WinKind, Status: PendingReference, Request: original.Request}} {
		waiting, unchanged, err := pending.Apply(current, ref, pending.CreatedAt())
		if err != nil || waiting.Status() != PendingReference || waiting.Direction() != "" || unchanged != current {
			t.Fatal(err)
		}
		completed, _, err := waiting.Apply(current, original, waiting.UpdatedAt().Add(time.Second))
		if err != nil || completed.Status() != Processed {
			t.Fatal(err)
		}
		expired, err := waiting.ExpireReference(current, waiting.UpdatedAt().Add(time.Minute))
		if err != nil || expired.FailureCode() != ReferenceNotFound {
			t.Fatal(err)
		}
	}
}
func TestRollbackHashAndInput(t *testing.T) {
	pending, _, _ := rollbackFixture(t, BetKind, "100.00")
	r := RollbackRequest{WinRequest: pending.Request()}
	hash, _ := r.PayloadHash()
	same := r
	same.Money, _ = money.New("00080.00", "BRL")
	same.IdempotencyKey = "other"
	equivalent, _ := same.PayloadHash()
	if hash != equivalent {
		t.Fatal("Normalização alterou identidade")
	}
	otherKind, _ := (RefundRequest{WinRequest: r.WinRequest}).PayloadHash()
	if hash == otherKind {
		t.Fatal("Tipos colidiram")
	}
	same.ReferenceExternalTransactionID = "other"
	changed, _ := same.PayloadHash()
	if hash == changed {
		t.Fatal("Hash ignorou referência")
	}
	for _, edit := range []func(*RollbackRequest){func(r *RollbackRequest) { r.ReferenceExternalTransactionID = "" }, func(r *RollbackRequest) { r.ReferenceExternalTransactionID = r.ExternalTransactionID }, func(r *RollbackRequest) { r.Money, _ = money.Zero("BRL") }} {
		invalid := r
		edit(&invalid)
		if _, err := NewRollback("invalid", invalid, pending.CreatedAt()); !errors.Is(err, ErrInvalidRollback) {
			t.Fatal("Entrada inválida aceita", err)
		}
	}
}
