package wager

import (
	"context"
	"fmt"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

// BetStore bloqueia a carteira e confirma resultado, saldo, ledger e outbox juntos.
// Não confirma PENDING: o processamento é síncrono e não deixa trabalho abandonado.
type BetStore interface {
	ProcessBet(context.Context, domainwager.Bet) (domainwager.Bet, bool, error)
}

type ProcessBet struct {
	store BetStore
	ids   applicationwallet.IDGenerator
	clock applicationwallet.Clock
}

func NewProcessBet(store BetStore, ids applicationwallet.IDGenerator, clock applicationwallet.Clock) *ProcessBet {
	return &ProcessBet{store: store, ids: ids, clock: clock}
}

func (uc *ProcessBet) Execute(ctx context.Context, input domainwager.BetRequest) (domainwager.Bet, bool, error) {
	if err := ctx.Err(); err != nil {
		return domainwager.Bet{}, false, err
	}
	if err := input.Validate(); err != nil {
		return domainwager.Bet{}, false, err
	}
	id, err := uc.ids.NewID()
	if err != nil {
		return domainwager.Bet{}, false, fmt.Errorf("gerar ID da aposta: %w", err)
	}
	b, err := domainwager.NewBet(id, input, uc.clock.Now())
	if err != nil {
		return domainwager.Bet{}, false, err
	}
	return uc.store.ProcessBet(ctx, b)
}
