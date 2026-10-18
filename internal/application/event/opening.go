package event

import (
	"encoding/json"
	"errors"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

var ErrInvalidEvent = errors.New("evento de abertura inválido")

// Snapshot expõe somente consultas e serialização do evento construído.
type Snapshot interface {
	json.Marshaler
	EventID() string
	EventType() string
	AggregateID() string
	OccurredAt() time.Time
}

type envelope[T any] struct {
	EventID       string    `json:"eventId"`
	EventType     string    `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          T         `json:"data"`
}

type openingProcessedData struct {
	TransactionID string      `json:"transactionId"`
	WalletID      string      `json:"walletId"`
	PlayerID      string      `json:"playerId"`
	Kind          string      `json:"kind"`
	Status        string      `json:"status"`
	Money         money.Money `json:"money"`
	Balance       money.Money `json:"balance"`
	WalletVersion int64       `json:"walletVersion"`
}

type walletBalanceChangedData struct {
	WalletID      string      `json:"walletId"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
}

type WagerTransactionProcessed struct {
	value envelope[openingProcessedData]
}
type WalletBalanceChanged struct {
	value envelope[walletBalanceChangedData]
}

func NewWagerTransactionProcessed(o wager.Opening) (WagerTransactionProcessed, error) {
	if err := o.Validate(); err != nil {
		return WagerTransactionProcessed{}, err
	}
	if o.Status() != wager.Processed {
		return WagerTransactionProcessed{}, ErrInvalidEvent
	}
	w := o.Wallet()
	return WagerTransactionProcessed{value: envelope[openingProcessedData]{
		EventID: "event:processed:" + o.ID(), EventType: "WagerTransactionProcessed",
		AggregateID: w.ID(), CorrelationID: o.ID(), CausationID: o.ID(), OccurredAt: w.CreatedAt(), Version: 1,
		Data: openingProcessedData{TransactionID: o.ID(), WalletID: w.ID(), PlayerID: w.PlayerID(),
			Kind: wager.OpeningKind, Status: wager.Processed, Money: w.Balance(), Balance: w.Balance(), WalletVersion: w.Version()},
	}}, nil
}

func NewWalletBalanceChanged(o wager.Opening, entry ledger.Entry) (WalletBalanceChanged, error) {
	if err := o.Validate(); err != nil {
		return WalletBalanceChanged{}, err
	}
	if err := entry.Validate(); err != nil {
		return WalletBalanceChanged{}, err
	}
	w := o.Wallet()
	if o.Status() != wager.Processed || entry.WalletID() != w.ID() || entry.TransactionID() != o.ID() ||
		entry.Direction() != ledger.Credit || entry.Amount() != w.Balance() || entry.BalanceAfter() != w.Balance() ||
		entry.BalanceBefore().MinorUnits() != 0 || !entry.CreatedAt().Equal(w.CreatedAt()) {
		return WalletBalanceChanged{}, ErrInvalidEvent
	}
	return WalletBalanceChanged{value: envelope[walletBalanceChangedData]{
		EventID: "event:balance:" + o.ID(), EventType: "WalletBalanceChanged",
		AggregateID: w.ID(), CorrelationID: o.ID(), CausationID: o.ID(), OccurredAt: w.CreatedAt(), Version: 1,
		Data: walletBalanceChangedData{WalletID: w.ID(), TransactionID: o.ID(), Direction: entry.Direction(),
			Money: entry.Amount(), BalanceBefore: entry.BalanceBefore(), BalanceAfter: entry.BalanceAfter(), WalletVersion: w.Version()},
	}}, nil
}

func (e WagerTransactionProcessed) MarshalJSON() ([]byte, error) { return json.Marshal(e.value) }
func (e WagerTransactionProcessed) EventID() string              { return e.value.EventID }
func (e WagerTransactionProcessed) EventType() string            { return e.value.EventType }
func (e WagerTransactionProcessed) AggregateID() string          { return e.value.AggregateID }
func (e WagerTransactionProcessed) OccurredAt() time.Time        { return e.value.OccurredAt }
func (e WalletBalanceChanged) MarshalJSON() ([]byte, error)      { return json.Marshal(e.value) }
func (e WalletBalanceChanged) EventID() string                   { return e.value.EventID }
func (e WalletBalanceChanged) EventType() string                 { return e.value.EventType }
func (e WalletBalanceChanged) AggregateID() string               { return e.value.AggregateID }
func (e WalletBalanceChanged) OccurredAt() time.Time             { return e.value.OccurredAt }
