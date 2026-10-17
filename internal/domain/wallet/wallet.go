package wallet

import (
	"fmt"
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
	if strings.TrimSpace(id) == "" {
		return Wallet{}, ErrInvalidID
	}
	if strings.TrimSpace(playerID) == "" {
		return Wallet{}, ErrInvalidPlayerID
	}
	if err := initialBalance.Validate(); err != nil {
		return Wallet{}, fmt.Errorf("saldo inicial: %w", err)
	}

	zero, err := money.Zero(initialBalance.Currency())
	if err != nil {
		return Wallet{}, fmt.Errorf("zero na moeda do saldo inicial: %w", err)
	}
	comparison, err := initialBalance.Compare(zero)
	if err != nil {
		return Wallet{}, fmt.Errorf("comparar saldo inicial: %w", err)
	}
	if comparison < 0 {
		return Wallet{}, ErrNegativeBalance
	}
	if createdAt.IsZero() {
		return Wallet{}, ErrInvalidTime
	}

	instant := createdAt.UTC()
	return Wallet{
		id:        id,
		playerID:  playerID,
		balance:   initialBalance,
		version:   1,
		createdAt: instant,
		updatedAt: instant,
	}, nil
}
