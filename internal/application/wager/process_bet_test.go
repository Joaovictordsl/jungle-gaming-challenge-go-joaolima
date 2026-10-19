package wager

import (
	"context"
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type testStore func(context.Context, domainwager.Bet) (domainwager.Bet, bool, error)

func (f testStore) ProcessBet(ctx context.Context, b domainwager.Bet) (domainwager.Bet, bool, error) {
	return f(ctx, b)
}

type testIDs struct{ err error }

func (ids testIDs) NewID() (string, error) { return "test-bet", ids.err }

type testClock struct{}

func (testClock) Now() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func TestProcessBetCoordination(t *testing.T) {
	value, _ := money.New("1.00", "BRL")
	input := domainwager.BetRequest{ProviderID: "p", ExternalTransactionID: "e", IdempotencyKey: "k", WalletID: "w", PlayerID: "u", RoundID: "r", GameID: "g", Money: value}
	cause := errors.New("store failure")
	calls := 0
	ctx := context.WithValue(context.Background(), struct{}{}, "request")
	store := testStore(func(got context.Context, b domainwager.Bet) (domainwager.Bet, bool, error) {
		calls++
		if got != ctx || b.ID() != "test-bet" || b.Status() != domainwager.Pending || b.Request() != input || !b.CreatedAt().Equal((testClock{}).Now()) {
			t.Fatal("Dependências e contexto não foram preservados")
		}
		return domainwager.Bet{}, false, cause
	})
	uc := NewProcessBet(store, testIDs{}, testClock{})
	if _, _, err := uc.Execute(ctx, input); !errors.Is(err, cause) || calls != 1 {
		t.Fatal(err)
	}
	invalid := input
	invalid.IdempotencyKey = ""
	if _, _, err := uc.Execute(ctx, invalid); err == nil || calls != 1 {
		t.Fatal("Entrada inválida chegou ao store")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := uc.Execute(canceled, input); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal(err)
	}
	uc = NewProcessBet(store, testIDs{err: cause}, testClock{})
	if _, _, err := uc.Execute(ctx, input); !errors.Is(err, cause) || calls != 1 {
		t.Fatal(err)
	}
}
