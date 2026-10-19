package wager

import (
	"context"
	"fmt"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type RollbackStore interface {
	ProcessRollback(context.Context, domainwager.Rollback) (domainwager.Rollback, bool, error)
}
type ProcessRollback struct {
	store RollbackStore
	ids   applicationwallet.IDGenerator
	clock applicationwallet.Clock
}

func NewProcessRollback(store RollbackStore, ids applicationwallet.IDGenerator, clock applicationwallet.Clock) *ProcessRollback {
	return &ProcessRollback{store: store, ids: ids, clock: clock}
}
func (uc *ProcessRollback) Execute(ctx context.Context, input domainwager.RollbackRequest) (domainwager.Rollback, bool, error) {
	if err := ctx.Err(); err != nil {
		return domainwager.Rollback{}, false, err
	}
	if err := input.Validate(); err != nil {
		return domainwager.Rollback{}, false, err
	}
	id, err := uc.ids.NewID()
	if err != nil {
		return domainwager.Rollback{}, false, fmt.Errorf("gerar ID de ROLLBACK: %w", err)
	}
	refund, err := domainwager.NewRollback(id, input, uc.clock.Now())
	if err != nil {
		return domainwager.Rollback{}, false, err
	}
	return uc.store.ProcessRollback(ctx, refund)
}
