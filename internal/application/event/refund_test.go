package event

import (
	"encoding/json"
	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"strings"
	"testing"
	"time"
)

func TestRefundTypedSnapshots(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	amount, _ := money.New("80.00", "BRL")
	balance, _ := money.New("20.00", "BRL")
	current, _ := wallet.NewWallet("w", "p", balance, at)
	r := wager.RefundRequest{WinRequest: wager.WinRequest{BetRequest: wager.BetRequest{ProviderID: "provider-a", ExternalTransactionID: "refund", IdempotencyKey: "key", WalletID: "w", PlayerID: "p", RoundID: "r", GameID: "g", Money: amount}, ReferenceExternalTransactionID: "bet"}}
	pending, err := wager.NewRefund("refund", r, at)
	if err != nil {
		t.Fatal(err)
	}
	ref := &wager.Reference{ID: "bet-id", Kind: wager.BetKind, Status: wager.Processed, Request: r.BetRequest}
	ref.Request.ExternalTransactionID = "bet"
	completed, _, err := pending.Apply(current, ref, at)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := NewRefundProcessed(completed)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := ledger.New("ledger", "w", completed.ID(), ledger.Credit, amount, current.Balance(), completed.Balance(), at)
	changed, err := NewRefundBalanceChanged(completed, entry)
	if err != nil {
		t.Fatal(err)
	}
	waiting, _, _ := pending.Apply(current, nil, at)
	waitingEvent, err := NewRefundPendingReference(waiting)
	if err != nil {
		t.Fatal(err)
	}
	ref.Reversed = true
	rejected, _, _ := pending.Apply(current, ref, at)
	rejectedEvent, err := NewRefundRejected(rejected)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, snapshot := range []Snapshot{processed, changed, waitingEvent, rejectedEvent} {
		data, err := json.Marshal(snapshot)
		if err != nil || snapshot.AggregateID() != "w" || ids[snapshot.EventID()] || (snapshot.EventType() != "WalletBalanceChanged" && !strings.Contains(string(data), `"kind":"REFUND"`)) || !strings.Contains(string(data), `"money":{"amount":"80.00","currency":"BRL"}`) || (snapshot.EventType() != "WalletBalanceChanged" && !strings.Contains(string(data), `"referenceExternalTransactionId":"bet"`)) {
			t.Fatalf("Snapshot inválido: %s %v", data, err)
		}
		ids[snapshot.EventID()] = true
	}
	if _, err := NewWinProcessed(completed); err == nil {
		t.Fatal("REFUND serializado como WIN")
	}
	if _, err := NewRefundProcessed(waiting); err == nil {
		t.Fatal("Pendência serializada como sucesso")
	}
	if _, err := NewRefundRejected(completed); err == nil {
		t.Fatal("Sucesso serializado como rejeição")
	}
}
