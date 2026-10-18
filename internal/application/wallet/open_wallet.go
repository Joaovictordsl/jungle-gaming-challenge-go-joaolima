package wallet

import (
	"context"
	"fmt"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	domainwallet "jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

// OpenWalletInput contém os dados necessários para solicitar a abertura de uma carteira.
type OpenWalletInput struct {
	PlayerID       string
	InitialBalance money.Money
}

// OpenWallet coordena a abertura de uma carteira usando dependências externas.
type OpenWallet struct {
	store OpeningStore
	ids   IDGenerator
	clock Clock
}

// NewOpenWallet recebe as dependências do caso de uso de abertura.
func NewOpenWallet(store OpeningStore, ids IDGenerator, clock Clock) *OpenWallet {
	return &OpenWallet{
		store: store,
		ids:   ids,
		clock: clock,
	}
}

// Execute cria uma carteira válida e solicita a persistência da abertura completa.
// O sucesso só é retornado depois que SaveOpening confirma a operação.
func (uc *OpenWallet) Execute(ctx context.Context, input OpenWalletInput) (domainwallet.Wallet, error) {
	if err := ctx.Err(); err != nil {
		return domainwallet.Wallet{}, err
	}

	id, err := uc.ids.NewID()
	if err != nil {
		return domainwallet.Wallet{}, fmt.Errorf("gerar identificador da carteira: %w", err)
	}

	w, err := domainwallet.NewWallet(id, input.PlayerID, input.InitialBalance, uc.clock.Now())
	if err != nil {
		return domainwallet.Wallet{}, fmt.Errorf("abrir carteira: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return domainwallet.Wallet{}, err
	}
	if err := uc.store.SaveOpening(ctx, w); err != nil {
		return domainwallet.Wallet{}, fmt.Errorf("persistir abertura da carteira: %w", err)
	}

	return w, nil
}
