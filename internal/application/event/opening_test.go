package event

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func TestOpeningEventsAreTypedStableSnapshots(t *testing.T) {
	balance, _ := money.New("100.05", "BRL")
	zero, _ := money.Zero("BRL")
	now := time.Date(2026, 10, 18, 13, 30, 0, 0, time.UTC)
	w, _ := wallet.NewWallet("wallet-1", "player-1", balance, now)
	pending, _ := wager.NewOpening(w)
	if _, err := NewWagerTransactionProcessed(pending); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Uma abertura pendente não pode emitir evento de sucesso: %v", err)
	}
	opening, _ := pending.Process()
	entry, _ := ledger.New("entry-1", w.ID(), opening.ID(), ledger.Credit, balance, zero, balance, now)
	processed, err := NewWagerTransactionProcessed(opening)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := NewWalletBalanceChanged(opening, entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []Snapshot{processed, changed} {
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			EventID, EventType, AggregateID, CorrelationID, CausationID string
			OccurredAt                                                  time.Time
			Version                                                     int
			Data                                                        struct {
				TransactionID, WalletID, Direction string
				WalletVersion                      int64
				Money                              struct{ Amount, Currency string }
				BalanceBefore, BalanceAfter        struct{ Amount, Currency string }
			}
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.EventID != snapshot.EventID() || decoded.EventType != snapshot.EventType() ||
			decoded.AggregateID != w.ID() || decoded.CorrelationID != opening.ID() || decoded.CausationID != opening.ID() ||
			!decoded.OccurredAt.Equal(now) || decoded.Version != 1 || decoded.Data.WalletVersion != 1 ||
			decoded.Data.TransactionID != opening.ID() || decoded.Data.Money.Amount != "100.05" || decoded.Data.Money.Currency != "BRL" {
			t.Fatalf("Envelope ou dados incorretos: %s", encoded)
		}
		if snapshot.EventType() == "WalletBalanceChanged" &&
			(decoded.Data.Direction != ledger.Credit || decoded.Data.BalanceBefore.Amount != "0.00" || decoded.Data.BalanceAfter.Amount != "100.05") {
			t.Fatalf("Evento de saldo deve descrever o crédito inicial: %s", encoded)
		}
	}
	other, _ := ledger.New("entry-2", "other-wallet", opening.ID(), ledger.Credit, balance, zero, balance, now)
	if _, err := NewWalletBalanceChanged(opening, other); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Evento não pode combinar abertura e ledger de carteiras distintas: %v", err)
	}
	again, _ := NewWagerTransactionProcessed(opening)
	if again.EventID() != processed.EventID() || processed.EventID() == changed.EventID() {
		t.Fatal("Identidades devem ser estáveis e distintas entre tipos de evento")
	}
}
