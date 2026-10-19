package event

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func TestBetEventSnapshots(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	balance, _ := money.New("100.00", "BRL")
	w, _ := wallet.NewWallet("w", "p", balance, at)
	for _, amount := range []string{"25.00", "101.00"} {
		value, _ := money.New(amount, "BRL")
		pending, _ := wager.NewBet("tx", wager.BetRequest{ProviderID: "provider", ExternalTransactionID: "external", IdempotencyKey: "key", WalletID: "w", PlayerID: "p", RoundID: "r", GameID: "g", Money: value}, at)
		completed, _, err := pending.Apply(w, at)
		if err != nil {
			t.Fatal(err)
		}
		var snapshots []Snapshot
		if completed.Status() == wager.Processed {
			processed, err := NewBetProcessed(completed)
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := ledger.New("entry", "w", "tx", ledger.Debit, value, balance, completed.Balance(), at)
			changed, err := NewBetBalanceChanged(completed, entry)
			if err != nil {
				t.Fatal(err)
			}
			snapshots = []Snapshot{processed, changed}
			if _, err := NewBetRejected(completed); err == nil {
				t.Fatal("Processada não pode emitir rejeição")
			}
		} else {
			rejected, err := NewBetRejected(completed)
			if err != nil {
				t.Fatal(err)
			}
			snapshots = []Snapshot{rejected}
			if _, err := NewBetProcessed(completed); err == nil {
				t.Fatal("Rejeitada não pode emitir sucesso")
			}
		}
		for _, snapshot := range snapshots {
			data, err := json.Marshal(snapshot)
			if err != nil || snapshot.EventID() == "" || snapshot.AggregateID() != "w" || !snapshot.OccurredAt().Equal(at) || !strings.Contains(string(data), `"money":{"amount":"`+amount+`","currency":"BRL"}`) {
				t.Fatalf("Snapshot financeiro incorreto: %s %v", data, err)
			}
			if snapshot.EventType() != "WalletBalanceChanged" && !strings.Contains(string(data), `"providerId":"provider"`) {
				t.Fatal("Evento perdeu identidade do provedor")
			}
		}
		if _, err := NewBetProcessed(pending); err == nil {
			t.Fatal("PENDING não pode emitir sucesso")
		}
	}
}
