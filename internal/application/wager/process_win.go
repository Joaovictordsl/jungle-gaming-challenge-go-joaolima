package wager

import (
	"context"
	"fmt"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	domainwager "jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type WinStore interface {
	ProcessWin(context.Context, domainwager.Win) (domainwager.Win, bool, error)
}
type ReferenceProcessor interface {
	RetryDueReference(context.Context) (bool, error)
}

type ProcessWin struct {
	store WinStore
	ids   applicationwallet.IDGenerator
	clock applicationwallet.Clock
}

func NewProcessWin(store WinStore, ids applicationwallet.IDGenerator, clock applicationwallet.Clock) *ProcessWin {
	return &ProcessWin{store: store, ids: ids, clock: clock}
}
func (uc *ProcessWin) Execute(ctx context.Context, input domainwager.WinRequest) (domainwager.Win, bool, error) {
	if err := ctx.Err(); err != nil {
		return domainwager.Win{}, false, err
	}
	if err := input.Validate(); err != nil {
		return domainwager.Win{}, false, err
	}
	id, err := uc.ids.NewID()
	if err != nil {
		return domainwager.Win{}, false, fmt.Errorf("gerar ID do prêmio: %w", err)
	}
	win, err := domainwager.NewWin(id, input, uc.clock.Now())
	if err != nil {
		return domainwager.Win{}, false, err
	}
	return uc.store.ProcessWin(ctx, win)
}
