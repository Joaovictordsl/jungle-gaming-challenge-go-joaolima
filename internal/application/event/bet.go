package event

import (
	"encoding/json"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type betRejectedData struct {
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string      `json:"referenceTransactionId,omitempty"`
	TransactionID                  string      `json:"transactionId"`
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	WalletID                       string      `json:"walletId"`
	PlayerID                       string      `json:"playerId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Status                         string      `json:"status"`
	FailureCode                    string      `json:"failureCode"`
	Money                          money.Money `json:"money"`
	Balance                        money.Money `json:"balance"`
	WalletVersion                  int64       `json:"walletVersion"`
}

type WagerTransactionRejected struct{ value envelope[betRejectedData] }

func NewBetProcessed(b wager.Bet) (WagerTransactionProcessed, error) {
	if err := b.Validate(); err != nil {
		return WagerTransactionProcessed{}, err
	}
	if b.Status() != wager.Processed {
		return WagerTransactionProcessed{}, ErrInvalidEvent
	}
	r := b.Request()
	return WagerTransactionProcessed{value: envelope[openingProcessedData]{
		EventID: "event:processed:" + b.ID(), EventType: "WagerTransactionProcessed", AggregateID: r.WalletID,
		CorrelationID: b.ID(), CausationID: b.ID(), OccurredAt: b.UpdatedAt(), Version: 1,
		Data: openingProcessedData{TransactionID: b.ID(), ProviderID: r.ProviderID, ExternalTransactionID: r.ExternalTransactionID,
			WalletID: r.WalletID, PlayerID: r.PlayerID, RoundID: r.RoundID, GameID: r.GameID, Kind: wager.BetKind, Status: b.Status(), Money: r.Money, Balance: b.Balance(), WalletVersion: b.WalletVersion()},
	}}, nil
}

func NewBetBalanceChanged(b wager.Bet, entry ledger.Entry) (WalletBalanceChanged, error) {
	if err := b.Validate(); err != nil {
		return WalletBalanceChanged{}, err
	}
	if err := entry.Validate(); err != nil {
		return WalletBalanceChanged{}, err
	}
	r := b.Request()
	if b.Status() != wager.Processed || entry.WalletID() != r.WalletID || entry.TransactionID() != b.ID() ||
		entry.Direction() != ledger.Debit || entry.Amount() != r.Money || entry.BalanceBefore() != b.BalanceBefore() ||
		entry.BalanceAfter() != b.Balance() || !entry.CreatedAt().Equal(b.UpdatedAt()) {
		return WalletBalanceChanged{}, ErrInvalidEvent
	}
	return WalletBalanceChanged{value: envelope[walletBalanceChangedData]{
		EventID: "event:balance:" + b.ID(), EventType: "WalletBalanceChanged", AggregateID: r.WalletID,
		CorrelationID: b.ID(), CausationID: b.ID(), OccurredAt: b.UpdatedAt(), Version: 1,
		Data: walletBalanceChangedData{WalletID: r.WalletID, TransactionID: b.ID(), Direction: entry.Direction(), Money: r.Money,
			BalanceBefore: b.BalanceBefore(), BalanceAfter: b.Balance(), WalletVersion: b.WalletVersion()},
	}}, nil
}

func NewBetRejected(b wager.Bet) (WagerTransactionRejected, error) {
	if err := b.Validate(); err != nil {
		return WagerTransactionRejected{}, err
	}
	if b.Status() != wager.Rejected {
		return WagerTransactionRejected{}, ErrInvalidEvent
	}
	r := b.Request()
	return WagerTransactionRejected{value: envelope[betRejectedData]{
		EventID: "event:rejected:" + b.ID(), EventType: "WagerTransactionRejected", AggregateID: r.WalletID,
		CorrelationID: b.ID(), CausationID: b.ID(), OccurredAt: b.UpdatedAt(), Version: 1,
		Data: betRejectedData{TransactionID: b.ID(), ProviderID: r.ProviderID, ExternalTransactionID: r.ExternalTransactionID,
			WalletID: r.WalletID, PlayerID: r.PlayerID, RoundID: r.RoundID, GameID: r.GameID, Kind: wager.BetKind, Status: b.Status(),
			FailureCode: b.FailureCode(), Money: r.Money, Balance: b.Balance(), WalletVersion: b.WalletVersion()},
	}}, nil
}

func (e WagerTransactionRejected) MarshalJSON() ([]byte, error) { return json.Marshal(e.value) }
func (e WagerTransactionRejected) EventID() string              { return e.value.EventID }
func (e WagerTransactionRejected) EventType() string            { return e.value.EventType }
func (e WagerTransactionRejected) AggregateID() string          { return e.value.AggregateID }
func (e WagerTransactionRejected) OccurredAt() time.Time        { return e.value.OccurredAt }
