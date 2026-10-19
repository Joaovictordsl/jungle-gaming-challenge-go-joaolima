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

func TestWinTypedEventsAndPendingSnapshot(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	amount, _ := money.New("25.00", "BRL")
	balance, _ := money.New("100.00", "BRL")
	current, _ := wallet.NewWallet("w", "p", balance, at)
	input := wager.WinRequest{BetRequest: wager.BetRequest{ProviderID: "provider-a", ExternalTransactionID: "win-1", IdempotencyKey: "key", WalletID: "w", PlayerID: "p", RoundID: "r", GameID: "g", Money: amount}}
	pending, _ := wager.NewWin("win", input, at)
	completed, _, err := pending.Apply(current, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := NewWinProcessed(completed)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := ledger.New("ledger", "w", "win", ledger.Credit, amount, current.Balance(), completed.Balance(), at)
	changed, err := NewWinBalanceChanged(completed, entry)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := []Snapshot{processed, changed}
	input.ReferenceExternalTransactionID = "missing-bet"
	pending, _ = wager.NewWin("pending", input, at)
	waiting, _, _ := pending.Apply(current, nil, at)
	referenceEvent, err := NewWinPendingReference(waiting)
	if err != nil {
		t.Fatal(err)
	}
	snapshots = append(snapshots, referenceEvent)
	expired, _ := waiting.ExpireReference(current, at.Add(time.Minute))
	rejected, err := NewWinRejected(expired)
	if err != nil {
		t.Fatal(err)
	}
	snapshots = append(snapshots, rejected)
	for _, snapshot := range snapshots {
		data, err := json.Marshal(snapshot)
		if err != nil || snapshot.EventID() == "" || snapshot.AggregateID() != "w" || !strings.Contains(string(data), `"money":{"amount":"25.00","currency":"BRL"}`) {
			t.Fatalf("Evento inválido: %s %v", data, err)
		}
	}
	if _, err := NewWinProcessed(waiting); err == nil {
		t.Fatal("Pendência emitiu sucesso")
	}
	if _, err := NewWinPendingReference(completed); err == nil {
		t.Fatal("Sucesso emitiu pendência")
	}
	if _, err := NewWinRejected(completed); err == nil {
		t.Fatal("Sucesso emitiu rejeição")
	}
}
