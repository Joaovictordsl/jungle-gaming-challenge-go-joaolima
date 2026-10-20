package wager

import (
	"encoding/json"
	"jungle-gaming-challenge-go-joaolima/internal/contract/strictjson"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type Command struct {
	wager.BetRequest
	ReferenceExternalTransactionID string
	Kind                           string
}

func DecodeCommand(body []byte, key string) (Command, string, error) {
	fields, err := strictjson.Fields(body, []string{"providerId", "externalTransactionId", "playerId", "walletId", "roundId", "gameId", "kind", "money"}, []string{"referenceExternalTransactionId"})
	if err != nil {
		return Command{}, "", err
	}
	values := make(map[string]string, 7)
	for _, name := range []string{"providerId", "externalTransactionId", "playerId", "walletId", "roundId", "gameId", "kind"} {
		var value *string
		if json.Unmarshal(fields[name], &value) != nil || value == nil {
			return Command{}, "", strictjson.ErrInvalid
		}
		values[name] = *value
	}
	if values["kind"] != wager.BetKind && values["kind"] != wager.WinKind && values["kind"] != wager.LossKind && values["kind"] != wager.RefundKind && values["kind"] != wager.RollbackKind {
		return Command{}, "", wager.ErrInvalidBet
	}
	var reference string
	if raw, exists := fields["referenceExternalTransactionId"]; exists {
		var value *string
		if (values["kind"] != wager.WinKind && values["kind"] != wager.RefundKind && values["kind"] != wager.RollbackKind) || json.Unmarshal(raw, &value) != nil || value == nil || *value == "" {
			return Command{}, "", wager.ErrInvalidWin
		}
		reference = *value
	}
	financial, err := strictjson.Object(fields["money"], "amount", "currency")
	if err != nil {
		return Command{}, "", err
	}
	var amount, currency *string
	if json.Unmarshal(financial["amount"], &amount) != nil || json.Unmarshal(financial["currency"], &currency) != nil || amount == nil || currency == nil {
		return Command{}, "", strictjson.ErrInvalid
	}
	value, err := money.New(*amount, *currency)
	if err != nil {
		return Command{}, "", err
	}
	request := wager.BetRequest{ProviderID: values["providerId"], ExternalTransactionID: values["externalTransactionId"], IdempotencyKey: key,
		PlayerID: values["playerId"], WalletID: values["walletId"], RoundID: values["roundId"], GameID: values["gameId"], Money: value}
	decoded := Command{BetRequest: request, ReferenceExternalTransactionID: reference, Kind: values["kind"]}
	switch values["kind"] {
	case wager.RollbackKind:
		err = (wager.RollbackRequest{WinRequest: wager.WinRequest{BetRequest: request, ReferenceExternalTransactionID: reference}}).Validate()
	case wager.RefundKind:
		err = (wager.RefundRequest{WinRequest: wager.WinRequest{BetRequest: request, ReferenceExternalTransactionID: reference}}).Validate()
	case wager.LossKind:
		err = (wager.LossRequest{BetRequest: request}).Validate()
	case wager.WinKind:
		err = (wager.WinRequest{BetRequest: request, ReferenceExternalTransactionID: reference}).Validate()
	default:
		err = request.Validate()
	}
	return decoded, values["kind"], err
}
