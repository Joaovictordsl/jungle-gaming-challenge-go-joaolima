package wager

import (
	"context"
	"errors"
	"testing"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type testWinStore func(context.Context, domainwager.Win) (domainwager.Win, bool, error)

func (f testWinStore) ProcessWin(ctx context.Context, w domainwager.Win) (domainwager.Win, bool, error) {
	return f(ctx, w)
}
func TestProcessWinCoordination(t *testing.T) {
	value, _ := money.New("25.00", "BRL")
	input := domainwager.WinRequest{BetRequest: domainwager.BetRequest{ProviderID: "p", ExternalTransactionID: "e", IdempotencyKey: "k", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Money: value}}
	cause := errors.New("store error")
	calls := 0
	ctx := context.WithValue(context.Background(), struct{}{}, "request")
	store := testWinStore(func(got context.Context, w domainwager.Win) (domainwager.Win, bool, error) {
		calls++
		if got != ctx || w.Request() != input || w.Status() != domainwager.Pending {
			t.Fatal("Entrada e contexto não preservados")
		}
		return domainwager.Win{}, false, cause
	})
	uc := NewProcessWin(store, testIDs{}, testClock{})
	if _, _, err := uc.Execute(ctx, input); !errors.Is(err, cause) || calls != 1 {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := uc.Execute(canceled, input); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal(err)
	}
	invalid := input
	invalid.IdempotencyKey = ""
	if _, _, err := uc.Execute(ctx, invalid); err == nil || calls != 1 {
		t.Fatal("Entrada inválida alcançou store")
	}
	uc = NewProcessWin(store, testIDs{err: cause}, testClock{})
	if _, _, err := uc.Execute(ctx, input); !errors.Is(err, cause) || calls != 1 {
		t.Fatal(err)
	}
}
