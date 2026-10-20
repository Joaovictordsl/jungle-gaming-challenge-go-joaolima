package wager

import (
	"context"
	"jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	domain "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type Stores interface {
	BetStore
	WinStore
	LossStore
	RefundStore
	RollbackStore
}
type Result struct {
	TransactionID   string
	Status          string
	FailureCode     string
	Balance         money.Money
	WalletVersion   int64
	FinancialReplay bool
}
type Dispatcher struct {
	ids   wallet.IDGenerator
	clock wallet.Clock
}

func NewDispatcher(ids wallet.IDGenerator, clock wallet.Clock) *Dispatcher {
	return &Dispatcher{ids: ids, clock: clock}
}
func (d *Dispatcher) Execute(ctx context.Context, stores Stores, c Command) (Result, error) {
	var result Result
	switch c.Kind {
	case domain.BetKind:
		t, replay, err := NewProcessBet(stores, d.ids, d.clock).Execute(ctx, c.BetRequest)
		if err != nil {
			return result, err
		}
		return Result{t.ID(), t.Status(), t.FailureCode(), t.Balance(), t.WalletVersion(), replay}, nil
	case domain.LossKind:
		t, replay, err := NewProcessLoss(stores, d.ids, d.clock).Execute(ctx, domain.LossRequest{BetRequest: c.BetRequest})
		if err != nil {
			return result, err
		}
		return Result{t.ID(), t.Status(), t.FailureCode(), t.Balance(), t.WalletVersion(), replay}, nil
	case domain.WinKind, domain.RefundKind, domain.RollbackKind:
		var t domain.ReferencedTransaction
		var replay bool
		var err error
		r := domain.WinRequest{BetRequest: c.BetRequest, ReferenceExternalTransactionID: c.ReferenceExternalTransactionID}
		switch c.Kind {
		case domain.WinKind:
			t, replay, err = NewProcessWin(stores, d.ids, d.clock).Execute(ctx, r)
		case domain.RefundKind:
			t, replay, err = NewProcessRefund(stores, d.ids, d.clock).Execute(ctx, domain.RefundRequest{WinRequest: r})
		case domain.RollbackKind:
			t, replay, err = NewProcessRollback(stores, d.ids, d.clock).Execute(ctx, domain.RollbackRequest{WinRequest: r})
		}
		if err != nil {
			return result, err
		}
		return Result{t.ID(), t.Status(), t.FailureCode(), t.Balance(), t.WalletVersion(), replay}, nil
	default:
		return result, domain.ErrInvalidState
	}
}
func (c Command) PayloadHash() (string, error) {
	if (c.Kind == domain.BetKind || c.Kind == domain.LossKind) && c.ReferenceExternalTransactionID != "" {
		return "", domain.ErrInvalidState
	}
	r := domain.WinRequest{BetRequest: c.BetRequest, ReferenceExternalTransactionID: c.ReferenceExternalTransactionID}
	switch c.Kind {
	case domain.BetKind:
		return c.BetRequest.PayloadHash()
	case domain.LossKind:
		return (domain.LossRequest{BetRequest: c.BetRequest}).PayloadHash()
	case domain.WinKind:
		return r.PayloadHash()
	case domain.RefundKind:
		return (domain.RefundRequest{WinRequest: r}).PayloadHash()
	case domain.RollbackKind:
		return (domain.RollbackRequest{WinRequest: r}).PayloadHash()
	}
	return "", domain.ErrInvalidState
}
func (c Command) Fields() map[string]any {
	f := map[string]any{"providerId": c.ProviderID, "externalTransactionId": c.ExternalTransactionID, "idempotencyKey": c.IdempotencyKey, "walletId": c.WalletID, "playerId": c.PlayerID, "roundId": c.RoundID, "gameId": c.GameID, "kind": c.Kind, "money": c.Money}
	if c.ReferenceExternalTransactionID != "" {
		f["referenceExternalTransactionId"] = c.ReferenceExternalTransactionID
	}
	return f
}
