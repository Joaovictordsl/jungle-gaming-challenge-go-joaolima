package wager

import (
	"context"
	"errors"
	"testing"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type testRollbackStore func(context.Context, domainwager.Rollback) (domainwager.Rollback, bool, error)

func (f testRollbackStore) ProcessRollback(ctx context.Context, r domainwager.Rollback) (domainwager.Rollback, bool, error) {
	return f(ctx, r)
}
func TestProcessRollbackCoordination(t *testing.T) {
	amount, _ := money.New("80.00", "BRL")
	input := domainwager.RollbackRequest{WinRequest: domainwager.WinRequest{BetRequest: domainwager.BetRequest{ProviderID: "p", ExternalTransactionID: "e", IdempotencyKey: "k", WalletID: "w", PlayerID: "u", RoundID: "r", GameID: "g", Money: amount}, ReferenceExternalTransactionID: "bet"}}
	cause := errors.New("store failure")
	calls := 0
	ctx := context.WithValue(context.Background(), struct{}{}, "request")
	store := testRollbackStore(func(got context.Context, r domainwager.Rollback) (domainwager.Rollback, bool, error) {
		calls++
		if got != ctx || r.Kind() != domainwager.RollbackKind || r.Status() != domainwager.Pending || r.Request() != input.WinRequest {
			t.Fatal("Entrada alterada")
		}
		return domainwager.Rollback{}, false, cause
	})
	uc := NewProcessRollback(store, testIDs{}, testClock{})
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
	uc = NewProcessRollback(store, testIDs{err: cause}, testClock{})
	if _, _, err := uc.Execute(ctx, input); !errors.Is(err, cause) || calls != 1 {
		t.Fatal(err)
	}
}
