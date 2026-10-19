package wager

import (
	"context"
	"errors"
	"testing"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type testLossStore func(context.Context, domainwager.Loss) (domainwager.Loss, bool, error)

func (f testLossStore) ProcessLoss(ctx context.Context, l domainwager.Loss) (domainwager.Loss, bool, error) {
	return f(ctx, l)
}
func TestProcessLossCoordination(t *testing.T) {
	zero, _ := money.Zero("BRL")
	input := domainwager.LossRequest{BetRequest: domainwager.BetRequest{ProviderID: "p", ExternalTransactionID: "e", IdempotencyKey: "k", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Money: zero}}
	calls := 0
	cause := errors.New("store failure")
	ctx := context.WithValue(context.Background(), struct{}{}, "request")
	store := testLossStore(func(got context.Context, l domainwager.Loss) (domainwager.Loss, bool, error) {
		calls++
		if got != ctx || l.Request() != input || l.Status() != domainwager.Pending || !l.CreatedAt().Equal((testClock{}).Now()) {
			t.Fatal("Contexto, entrada ou dependências alterados")
		}
		return domainwager.Loss{}, false, cause
	})
	uc := NewProcessLoss(store, testIDs{}, testClock{})
	if _, _, err := uc.Execute(ctx, input); !errors.Is(err, cause) || calls != 1 {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := uc.Execute(canceled, input); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal(err)
	}
	invalid := input
	invalid.Money, _ = money.New("0.01", "BRL")
	if _, _, err := uc.Execute(ctx, invalid); err == nil || calls != 1 {
		t.Fatal("Entrada inválida chegou ao store")
	}
	uc = NewProcessLoss(store, testIDs{err: cause}, testClock{})
	if _, _, err := uc.Execute(ctx, input); !errors.Is(err, cause) || calls != 1 {
		t.Fatal(err)
	}
}
