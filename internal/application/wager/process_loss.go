package wager

import (
	"context"
	"fmt"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type LossStore interface {
	ProcessLoss(context.Context, domainwager.Loss) (domainwager.Loss, bool, error)
}
type ProcessLoss struct {
	store LossStore
	ids   applicationwallet.IDGenerator
	clock applicationwallet.Clock
}

func NewProcessLoss(store LossStore, ids applicationwallet.IDGenerator, clock applicationwallet.Clock) *ProcessLoss {
	return &ProcessLoss{store: store, ids: ids, clock: clock}
}
func (uc *ProcessLoss) Execute(ctx context.Context, input domainwager.LossRequest) (domainwager.Loss, bool, error) {
	if err := ctx.Err(); err != nil {
		return domainwager.Loss{}, false, err
	}
	if err := input.Validate(); err != nil {
		return domainwager.Loss{}, false, err
	}
	id, err := uc.ids.NewID()
	if err != nil {
		return domainwager.Loss{}, false, fmt.Errorf("gerar ID de LOSS: %w", err)
	}
	loss, err := domainwager.NewLoss(id, input, uc.clock.Now())
	if err != nil {
		return domainwager.Loss{}, false, err
	}
	return uc.store.ProcessLoss(ctx, loss)
}
