//go:build integration

package postgres

import (
	"strings"
	"testing"
)

func TestMetricsCountCommittedTransactionsRatherThanReplaysIntegration(t *testing.T) {
	ctx, pool := failedTestDatabase(t)
	w := repositoryTestWallet(t, "metrics-wallet", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	bet := pendingTestBet(t, w, "bet", "key", "80.00")
	for range 3 {
		if _, _, err := NewBetStore(pool).ProcessBet(ctx, bet); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "rejected", "rejected-key", "80.00")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := MetricsSnapshot(pool)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`jungle_wager_transactions{kind="OPENING",status="PROCESSED"} 1`,
		`jungle_wager_transactions{kind="BET",status="PROCESSED"} 1`,
		`jungle_wager_transactions{kind="BET",status="REJECTED"} 1`,
		`jungle_wager_transactions{kind="BET",status="FAILED"} 0`,
		`jungle_outbox_unpublished 5`,
	} {
		if !strings.Contains(snapshot, want+"\n") {
			t.Fatal("snapshot financeiro incorreto", want)
		}
	}
	if strings.Contains(snapshot, w.ID()) || strings.Contains(snapshot, "provider-a") {
		t.Fatal("snapshot contém labels de identidade")
	}
}
