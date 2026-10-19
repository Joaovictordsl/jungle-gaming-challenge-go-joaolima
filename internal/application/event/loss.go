package event

import "jungle-gaming-challenge-go-joaolima/internal/domain/wager"

func NewLossProcessed(l wager.Loss) (WagerTransactionProcessed, error) {
	if err := l.Validate(); err != nil {
		return WagerTransactionProcessed{}, err
	}
	if l.Status() != wager.Processed {
		return WagerTransactionProcessed{}, ErrInvalidEvent
	}
	r := l.Request()
	return WagerTransactionProcessed{value: envelope[openingProcessedData]{
		EventID: "event:processed:" + l.ID(), EventType: "WagerTransactionProcessed", AggregateID: r.WalletID,
		CorrelationID: l.ID(), CausationID: l.ID(), OccurredAt: l.UpdatedAt(), Version: 1,
		Data: openingProcessedData{TransactionID: l.ID(), ProviderID: r.ProviderID, ExternalTransactionID: r.ExternalTransactionID,
			WalletID: r.WalletID, PlayerID: r.PlayerID, RoundID: r.RoundID, GameID: r.GameID, Kind: wager.LossKind, Status: l.Status(),
			Money: r.Money, Balance: l.Balance(), WalletVersion: l.WalletVersion()},
	}}, nil
}
