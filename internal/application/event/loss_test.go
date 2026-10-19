package event

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func TestLossProcessedEventPreservesZeroAndSnapshot(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero, _ := money.Zero("BRL")
	balance, _ := money.New("100.00", "BRL")
	current, _ := wallet.NewWallet("w", "p", balance, at)
	input := wager.LossRequest{BetRequest: wager.BetRequest{ProviderID: "provider-a", ExternalTransactionID: "loss-1", IdempotencyKey: "key", WalletID: "w", PlayerID: "p", RoundID: "round", GameID: "game", Money: zero}}
	pending, _ := wager.NewLoss("loss", input, at)
	completed, err := pending.Process(current, at)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewLossProcessed(completed)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil || snapshot.EventType() != "WagerTransactionProcessed" || snapshot.EventID() != "event:processed:loss" || snapshot.AggregateID() != "w" || !snapshot.OccurredAt().Equal(at) {
		t.Fatal("Envelope inválido", err)
	}
	for _, part := range []string{`"kind":"LOSS"`, `"money":{"amount":"0.00","currency":"BRL"}`, `"balance":{"amount":"100.00","currency":"BRL"}`, `"walletVersion":1`, `"providerId":"provider-a"`} {
		if !strings.Contains(string(data), part) {
			t.Fatalf("Snapshot perdeu %s: %s", part, data)
		}
	}
	if _, err := NewLossProcessed(pending); err == nil {
		t.Fatal("PENDING emitiu evento de sucesso")
	}
	if _, err := NewLossProcessed(wager.Loss{}); err == nil {
		t.Fatal("Zero value emitiu evento")
	}
}
