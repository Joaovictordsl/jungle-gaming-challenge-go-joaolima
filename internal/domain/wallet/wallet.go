package wallet

import (
	"fmt"
	"math"
	"strings"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

type Wallet struct {
	id        string
	playerID  string
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// NewWallet cria o estado inicial da carteira. A persistência e o ledger serão
// coordenados pelo caso de uso de abertura em uma única transação SQL.
func NewWallet(id string, playerID string, initialBalance money.Money, createdAt time.Time) (Wallet, error) {
	instant := createdAt.UTC()
	w := Wallet{
		id:        id,
		playerID:  playerID,
		balance:   initialBalance,
		version:   1,
		createdAt: instant,
		updatedAt: instant,
	}
	if err := w.Validate(); err != nil {
		return Wallet{}, err
	}
	return w, nil
}

// Rehydrate reconstrói o estado persistido, sem movimentar saldo, alterar versão
// ou emitir eventos. Os instantes são preservados e representados em UTC.
func Rehydrate(id string, playerID string, balance money.Money, version int64, createdAt time.Time, updatedAt time.Time) (Wallet, error) {
	w := Wallet{
		id:        id,
		playerID:  playerID,
		balance:   balance,
		version:   version,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}
	if err := w.Validate(); err != nil {
		return Wallet{}, err
	}
	return w, nil
}

// Validate verifica as invariantes compartilhadas por criação e reidratação.
func (w Wallet) Validate() error {
	if strings.TrimSpace(w.id) == "" {
		return ErrInvalidID
	}
	if strings.TrimSpace(w.playerID) == "" {
		return ErrInvalidPlayerID
	}
	if err := w.balance.Validate(); err != nil {
		return fmt.Errorf("saldo da carteira: %w", err)
	}

	zero, err := money.Zero(w.balance.Currency())
	if err != nil {
		return fmt.Errorf("zero na moeda do saldo: %w", err)
	}
	comparison, err := w.balance.Compare(zero)
	if err != nil {
		return fmt.Errorf("comparar saldo: %w", err)
	}
	if comparison < 0 {
		return ErrNegativeBalance
	}
	if w.version < 1 {
		return ErrInvalidVersion
	}
	if w.createdAt.IsZero() || w.updatedAt.IsZero() || w.updatedAt.Before(w.createdAt) {
		return ErrInvalidTime
	}
	return nil
}

// ValidateInitialState impede usar uma carteira já movimentada como nova abertura.
func (w Wallet) ValidateInitialState() error {
	if err := w.Validate(); err != nil {
		return err
	}
	if w.version != 1 || !w.createdAt.Equal(w.updatedAt) {
		return ErrNotInitialState
	}
	return nil
}

func (w Wallet) ID() string {
	return w.id
}

func (w Wallet) PlayerID() string {
	return w.playerID
}

func (w Wallet) Balance() money.Money {
	return w.balance
}

func (w Wallet) Version() int64 {
	return w.version
}

func (w Wallet) CreatedAt() time.Time {
	return w.createdAt
}

func (w Wallet) UpdatedAt() time.Time {
	return w.updatedAt
}

// Debit devolve um novo estado; persistência e ledger pertencem à mesma transação SQL.
func (w Wallet) Debit(amount money.Money, at time.Time) (Wallet, error) {
	if err := w.Validate(); err != nil {
		return Wallet{}, err
	}
	if err := amount.Validate(); err != nil {
		return Wallet{}, err
	}
	if amount.MinorUnits() <= 0 {
		return Wallet{}, ErrInvalidMovement
	}
	if at.IsZero() || at.Before(w.updatedAt) {
		return Wallet{}, ErrInvalidTime
	}
	comparison, err := w.balance.Compare(amount)
	if err != nil {
		return Wallet{}, err
	}
	if comparison < 0 {
		return Wallet{}, ErrInsufficientFunds
	}
	if w.version == math.MaxInt64 {
		return Wallet{}, ErrInvalidVersion
	}
	balance, err := w.balance.Subtract(amount)
	if err != nil {
		return Wallet{}, err
	}
	w.balance, w.version, w.updatedAt = balance, w.version+1, at.UTC()
	return w, nil
}

// Credit devolve um novo estado e verifica overflow antes de alterar saldo e versão.
func (w Wallet) Credit(amount money.Money, at time.Time) (Wallet, error) {
	if err := w.Validate(); err != nil {
		return Wallet{}, err
	}
	if err := amount.Validate(); err != nil {
		return Wallet{}, err
	}
	if amount.MinorUnits() <= 0 {
		return Wallet{}, ErrInvalidMovement
	}
	if at.IsZero() || at.Before(w.updatedAt) {
		return Wallet{}, ErrInvalidTime
	}
	balance, err := w.balance.Add(amount)
	if err != nil {
		return Wallet{}, err
	}
	if w.version == math.MaxInt64 {
		return Wallet{}, ErrInvalidVersion
	}
	w.balance, w.version, w.updatedAt = balance, w.version+1, at.UTC()
	return w, nil
}
