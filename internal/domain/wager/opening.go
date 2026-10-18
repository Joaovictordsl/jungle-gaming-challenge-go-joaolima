package wager

import (
	"errors"

	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

const (
	OpeningKind = "OPENING"
	Pending     = "PENDING"
	Processed   = "PROCESSED"
)

var (
	ErrInvalidOpening = errors.New("abertura financeira inválida")
	ErrInvalidState   = errors.New("transição de estado inválida")
)

// Opening representa exclusivamente a transação interna de crédito inicial.
// Seu estado é imutável: Process devolve uma nova cópia.
type Opening struct {
	wallet wallet.Wallet
	status string
}

func NewOpening(w wallet.Wallet) (Opening, error) {
	if err := w.ValidateInitialState(); err != nil {
		return Opening{}, err
	}
	if w.Balance().MinorUnits() <= 0 {
		return Opening{}, ErrInvalidOpening
	}
	return Opening{wallet: w, status: Pending}, nil
}

func (o Opening) Validate() error {
	if err := o.wallet.ValidateInitialState(); err != nil {
		return err
	}
	if o.wallet.Balance().MinorUnits() <= 0 || (o.status != Pending && o.status != Processed) {
		return ErrInvalidOpening
	}
	return nil
}

func (o Opening) Process() (Opening, error) {
	if err := o.Validate(); err != nil {
		return Opening{}, err
	}
	if o.status != Pending {
		return Opening{}, ErrInvalidState
	}
	o.status = Processed
	return o, nil
}

// Uma carteira só pode ter uma abertura. A identidade deriva de sua identidade
// estável e permanece a mesma em toda tentativa de persistência.
func (o Opening) ID() string            { return "opening:" + o.wallet.ID() }
func (o Opening) Status() string        { return o.status }
func (o Opening) Wallet() wallet.Wallet { return o.wallet }
