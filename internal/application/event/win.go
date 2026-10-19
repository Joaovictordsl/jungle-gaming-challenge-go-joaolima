package event

import (
	"encoding/json"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

func referencedData(w wager.ReferencedTransaction) openingProcessedData {
	r := w.Request()
	return openingProcessedData{TransactionID: w.ID(), ProviderID: r.ProviderID, ExternalTransactionID: r.ExternalTransactionID,
		WalletID: r.WalletID, PlayerID: r.PlayerID, RoundID: r.RoundID, GameID: r.GameID, Kind: w.Kind(), Status: w.Status(),
		Money: r.Money, Balance: w.Balance(), WalletVersion: w.WalletVersion(), ReferenceExternalTransactionID: r.ReferenceExternalTransactionID, ReferenceTransactionID: w.ReferenceID()}
}
func NewReferencedProcessed(w wager.ReferencedTransaction) (WagerTransactionProcessed, error) {
	if err := w.Validate(); err != nil {
		return WagerTransactionProcessed{}, err
	}
	if w.Status() != wager.Processed {
		return WagerTransactionProcessed{}, ErrInvalidEvent
	}
	return WagerTransactionProcessed{value: envelope[openingProcessedData]{EventID: "event:processed:" + w.ID(), EventType: "WagerTransactionProcessed",
		AggregateID: w.Request().WalletID, CorrelationID: w.ID(), CausationID: w.ID(), OccurredAt: w.UpdatedAt(), Version: 1, Data: referencedData(w)}}, nil
}
func NewReferencedBalanceChanged(w wager.ReferencedTransaction, entry ledger.Entry) (WalletBalanceChanged, error) {
	if err := w.Validate(); err != nil {
		return WalletBalanceChanged{}, err
	}
	if err := entry.Validate(); err != nil {
		return WalletBalanceChanged{}, err
	}
	r := w.Request()
	if w.Status() != wager.Processed || entry.WalletID() != r.WalletID || entry.TransactionID() != w.ID() || entry.Direction() != w.Direction() ||
		entry.Amount() != r.Money || entry.BalanceBefore() != w.BalanceBefore() || entry.BalanceAfter() != w.Balance() || !entry.CreatedAt().Equal(w.UpdatedAt()) {
		return WalletBalanceChanged{}, ErrInvalidEvent
	}
	return WalletBalanceChanged{value: envelope[walletBalanceChangedData]{EventID: "event:balance:" + w.ID(), EventType: "WalletBalanceChanged",
		AggregateID: r.WalletID, CorrelationID: w.ID(), CausationID: w.ID(), OccurredAt: w.UpdatedAt(), Version: 1,
		Data: walletBalanceChangedData{WalletID: r.WalletID, TransactionID: w.ID(), Direction: w.Direction(), Money: r.Money,
			BalanceBefore: w.BalanceBefore(), BalanceAfter: w.Balance(), WalletVersion: w.WalletVersion()}}}, nil
}
func NewReferencedRejected(w wager.ReferencedTransaction) (WagerTransactionRejected, error) {
	if err := w.Validate(); err != nil {
		return WagerTransactionRejected{}, err
	}
	if w.Status() != wager.Rejected {
		return WagerTransactionRejected{}, ErrInvalidEvent
	}
	r := w.Request()
	return WagerTransactionRejected{value: envelope[betRejectedData]{EventID: "event:rejected:" + w.ID(), EventType: "WagerTransactionRejected",
		AggregateID: r.WalletID, CorrelationID: w.ID(), CausationID: w.ID(), OccurredAt: w.UpdatedAt(), Version: 1,
		Data: betRejectedData{TransactionID: w.ID(), ProviderID: r.ProviderID, ExternalTransactionID: r.ExternalTransactionID, WalletID: r.WalletID, PlayerID: r.PlayerID,
			RoundID: r.RoundID, GameID: r.GameID, Kind: w.Kind(), Status: w.Status(), FailureCode: w.FailureCode(), Money: r.Money, Balance: w.Balance(), WalletVersion: w.WalletVersion(),
			ReferenceExternalTransactionID: r.ReferenceExternalTransactionID, ReferenceTransactionID: w.ReferenceID()}}}, nil
}

type WagerTransactionPendingReference struct {
	value envelope[openingProcessedData]
}

func NewReferencedPendingReference(w wager.ReferencedTransaction) (WagerTransactionPendingReference, error) {
	if err := w.Validate(); err != nil {
		return WagerTransactionPendingReference{}, err
	}
	if w.Status() != wager.PendingReference {
		return WagerTransactionPendingReference{}, ErrInvalidEvent
	}
	return WagerTransactionPendingReference{value: envelope[openingProcessedData]{EventID: "event:pending-reference:" + w.ID(), EventType: "WagerTransactionPendingReference",
		AggregateID: w.Request().WalletID, CorrelationID: w.ID(), CausationID: w.ID(), OccurredAt: w.UpdatedAt(), Version: 1, Data: referencedData(w)}}, nil
}
func (e WagerTransactionPendingReference) MarshalJSON() ([]byte, error) { return json.Marshal(e.value) }
func (e WagerTransactionPendingReference) EventID() string              { return e.value.EventID }
func (e WagerTransactionPendingReference) EventType() string            { return e.value.EventType }
func (e WagerTransactionPendingReference) AggregateID() string          { return e.value.AggregateID }
func (e WagerTransactionPendingReference) OccurredAt() time.Time        { return e.value.OccurredAt }
