package wager

import (
	"context"
	"errors"
	"testing"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type testRefundStore func(context.Context, domainwager.Refund) (domainwager.Refund, bool, error)

func (f testRefundStore) ProcessRefund(ctx context.Context, r domainwager.Refund) (domainwager.Refund, bool, error) {
	return f(ctx, r)
}
func TestProcessRefundCoordination(t *testing.T) {
	amount, _ := money.New("80.00", "BRL")
	input := domainwager.RefundRequest{WinRequest: domainwager.WinRequest{BetRequest: domainwager.BetRequest{ProviderID: "p", ExternalTransactionID: "e", IdempotencyKey: "k", WalletID: "w", PlayerID: "u", RoundID: "r", GameID: "g", Money: amount}, ReferenceExternalTransactionID: "bet"}}
	cause := errors.New("store failure")
	calls := 0
	ctx := context.WithValue(context.Background(), struct{}{}, "request")
	store := testRefundStore(func(got context.Context, r domainwager.Refund) (domainwager.Refund, bool, error) {
		calls++
		if got != ctx || r.Kind() != domainwager.RefundKind || r.Status() != domainwager.Pending || r.Request() != input.WinRequest {
			t.Fatal("Entrada alterada")
		}
		return domainwager.Refund{}, false, cause
	})
	uc := NewProcessRefund(store, testIDs{}, testClock{})
	if _, _, err := uc.Execute(ctx, input); !errors.Is(err, cause) || calls != 1 {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := uc.Execute(canceled, input); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal(err)
	}
	invalid := input
	invalid.ReferenceExternalTransactionID = ""
	if _, _, err := uc.Execute(ctx, invalid); err == nil || calls != 1 {
		t.Fatal("Entrada inválida alcançou store")
	}
	uc = NewProcessRefund(store, testIDs{err: cause}, testClock{})
	if _, _, err := uc.Execute(ctx, input); !errors.Is(err, cause) || calls != 1 {
		t.Fatal(err)
	}
}
