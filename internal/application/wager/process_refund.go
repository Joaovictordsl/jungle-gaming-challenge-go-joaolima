package wager

import (
	"context"
	"fmt"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type RefundStore interface {
	ProcessRefund(context.Context, domainwager.Refund) (domainwager.Refund, bool, error)
}
type ProcessRefund struct {
	store RefundStore
	ids   applicationwallet.IDGenerator
	clock applicationwallet.Clock
}

func NewProcessRefund(store RefundStore, ids applicationwallet.IDGenerator, clock applicationwallet.Clock) *ProcessRefund {
	return &ProcessRefund{store: store, ids: ids, clock: clock}
}
func (uc *ProcessRefund) Execute(ctx context.Context, input domainwager.RefundRequest) (domainwager.Refund, bool, error) {
	if err := ctx.Err(); err != nil {
		return domainwager.Refund{}, false, err
	}
	if err := input.Validate(); err != nil {
		return domainwager.Refund{}, false, err
	}
	id, err := uc.ids.NewID()
	if err != nil {
		return domainwager.Refund{}, false, fmt.Errorf("gerar ID de REFUND: %w", err)
	}
	refund, err := domainwager.NewRefund(id, input, uc.clock.Now())
	if err != nil {
		return domainwager.Refund{}, false, err
	}
	return uc.store.ProcessRefund(ctx, refund)
}
