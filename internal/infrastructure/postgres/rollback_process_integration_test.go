//go:build integration

package postgres

import (
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"testing"
)

func TestRollbackAndRefundAcrossThreeIndependentProcessesIntegration(t *testing.T) {
	ctx, pool := rollbackTestDatabase(t)
	w := repositoryTestWallet(t, "distributed-rollback", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "bet", "bet-key", "80.00")); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, w.ID()); err != nil {
		t.Fatal(err)
	}
	inputs := []betProcessInput{
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "rollback-a", Key: "key-a", Kind: wager.RollbackKind, Amount: "80.00", Reference: "bet"},
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "refund-b", Key: "key-b", Kind: wager.RefundKind, Amount: "80.00", Reference: "bet"},
		{WalletID: w.ID(), PlayerID: w.PlayerID(), External: "rollback-a", Key: "key-a", Kind: wager.RollbackKind, Amount: "00080.00", Reference: "bet"},
	}
	results := runBetProcesses(t, ctx, pool, inputs, func() {
		if err := blocker.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	})
	unique := map[string]betProcessOutput{}
	replays := 0
	for _, r := range results {
		unique[r.TransactionID] = r
		if r.Replay {
			replays++
		}
	}
	processed, rejected := 0, 0
	for _, r := range unique {
		switch r.Status {
		case wager.Processed:
			processed++
		case wager.Rejected:
			rejected++
		}
	}
	if len(unique) != 2 || replays != 1 || processed != 1 || rejected != 1 {
		t.Fatalf("Devolução repetida entre processos: %+v", results)
	}
	var code string
	if err := pool.QueryRow(ctx, `SELECT failure_code FROM wager_transactions WHERE kind IN ('REFUND','ROLLBACK') AND status='REJECTED'`).Scan(&code); err != nil || code != wager.AlreadyReversed {
		t.Fatal("Rejeição não auditada", err)
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 10000 || loaded.Version() != 3 {
		t.Fatal("Processos duplicaram crédito", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 4, 3, 7})
	for _, r := range runBetProcesses(t, ctx, pool, inputs, nil) {
		original, ok := unique[r.TransactionID]
		if !ok || !r.Replay || r.Status != original.Status || r.Balance != original.Balance {
			t.Fatal("Reinício mudou resultado persistido")
		}
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 4, 3, 7})
}
