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

func TestRollbackTypedSnapshots(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	amount, _ := money.New("80.00", "BRL")
	balance, _ := money.New("20.00", "BRL")
	current, _ := wallet.NewWallet("w", "p", balance, at)
	r := wager.RollbackRequest{WinRequest: wager.WinRequest{BetRequest: wager.BetRequest{ProviderID: "provider-a", ExternalTransactionID: "rollback", IdempotencyKey: "key", WalletID: "w", PlayerID: "p", RoundID: "r", GameID: "g", Money: amount}, ReferenceExternalTransactionID: "bet"}}
	pending, err := wager.NewRollback("rollback", r, at)
	if err != nil {
		t.Fatal(err)
	}
	ref := &wager.Reference{ID: "bet-id", Kind: wager.BetKind, Status: wager.Processed, Request: r.BetRequest}
	ref.Request.ExternalTransactionID = "bet"
	completed, _, err := pending.Apply(current, ref, at)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := NewRollbackProcessed(completed)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := ledger.New("ledger", "w", completed.ID(), ledger.Credit, amount, current.Balance(), completed.Balance(), at)
	changed, err := NewRollbackBalanceChanged(completed, entry)
	if err != nil {
		t.Fatal(err)
	}
	waiting, _, _ := pending.Apply(current, nil, at)
	waitingEvent, err := NewRollbackPendingReference(waiting)
	if err != nil {
		t.Fatal(err)
	}
	ref.Reversed = true
	rejected, _, _ := pending.Apply(current, ref, at)
	rejectedEvent, err := NewRollbackRejected(rejected)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, snapshot := range []Snapshot{processed, changed, waitingEvent, rejectedEvent} {
		data, err := json.Marshal(snapshot)
		if err != nil || snapshot.AggregateID() != "w" || ids[snapshot.EventID()] || (snapshot.EventType() != "WalletBalanceChanged" && !strings.Contains(string(data), `"kind":"ROLLBACK"`)) || !strings.Contains(string(data), `"money":{"amount":"80.00","currency":"BRL"}`) || (snapshot.EventType() != "WalletBalanceChanged" && !strings.Contains(string(data), `"referenceExternalTransactionId":"bet"`)) {
			t.Fatalf("Snapshot inválido: %s %v", data, err)
		}
		ids[snapshot.EventID()] = true
	}
	if _, err := NewWinProcessed(completed); err == nil {
		t.Fatal("ROLLBACK serializado como WIN")
	}
	if _, err := NewRollbackProcessed(waiting); err == nil {
		t.Fatal("Pendência serializada como sucesso")
	}
	if _, err := NewRollbackRejected(completed); err == nil {
		t.Fatal("Sucesso serializado como rejeição")
	}
}

func TestRollbackDebitEventCarriesInverseDirection(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	amount, _ := money.New("80.00", "BRL")
	balance, _ := money.New("100.00", "BRL")
	current, _ := wallet.NewWallet("w", "p", balance, at)
	r := wager.RollbackRequest{WinRequest: wager.WinRequest{BetRequest: wager.BetRequest{ProviderID: "provider-a", ExternalTransactionID: "rollback", IdempotencyKey: "key", WalletID: "w", PlayerID: "p", RoundID: "r", GameID: "g", Money: amount}, ReferenceExternalTransactionID: "win"}}
	pending, _ := wager.NewRollback("rollback", r, at)
	ref := &wager.Reference{ID: "win-id", Kind: wager.WinKind, Status: wager.Processed, Request: r.BetRequest}
	ref.Request.ExternalTransactionID = "win"
	completed, _, err := pending.Apply(current, ref, at)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := ledger.New("ledger", "w", completed.ID(), ledger.Debit, amount, balance, completed.Balance(), at)
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewRollbackBalanceChanged(completed, entry)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(event)
	if err != nil || !strings.Contains(string(data), `"direction":"DEBIT"`) || !strings.Contains(string(data), `"balanceAfter":{"amount":"20.00","currency":"BRL"}`) {
		t.Fatalf("Evento não descreve débito: %s %v", data, err)
	}
}
