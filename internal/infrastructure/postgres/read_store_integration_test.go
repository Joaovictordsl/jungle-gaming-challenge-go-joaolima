//go:build integration

package postgres

import (
	"errors"
	"jungle-gaming-challenge-go-joaolima/internal/application/query"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"sync"
	"testing"
)

func TestReadTransactionsLedgerAndReconciliationIntegration(t *testing.T) {
	ctx, pool := rollbackTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000008_ledger_queries.up.sql")
	runOpeningMigration(t, ctx, pool, "000008_ledger_queries.down.sql")
	runOpeningMigration(t, ctx, pool, "000008_ledger_queries.up.sql")
	w := repositoryTestWallet(t, "read-wallet", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	bet, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "bet", "bet-key", "25.00"))
	if err != nil {
		t.Fatal(err)
	}
	win, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "win", "win-key", "10.00", "bet"))
	if err != nil {
		t.Fatal(err)
	}
	loss, _, err := NewLossStore(pool).ProcessLoss(ctx, pendingTestLoss(t, w, "loss", "loss-key"))
	if err != nil {
		t.Fatal(err)
	}
	refund, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, "refund", "refund-key", "25.00", "bet"))
	if err != nil {
		t.Fatal(err)
	}
	rb, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, "rollback", "rollback-key", "10.00", "win"))
	if err != nil {
		t.Fatal(err)
	}
	rejected, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "rejected", "rejected-key", "1000.00"))
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, "pending", "pending-key", "1.00", "absent"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewReadStore(pool)
	for _, tc := range []struct{ id, kind, status, external string }{{"opening:" + w.ID(), "OPENING", wager.Processed, ""}, {bet.ID(), wager.BetKind, wager.Processed, "bet"}, {win.ID(), wager.WinKind, wager.Processed, "win"}, {loss.ID(), wager.LossKind, wager.Processed, "loss"}, {refund.ID(), wager.RefundKind, wager.Processed, "refund"}, {rb.ID(), wager.RollbackKind, wager.Processed, "rollback"}, {rejected.ID(), wager.BetKind, wager.Rejected, "rejected"}, {pending.ID(), wager.RefundKind, wager.PendingReference, "pending"}} {
		result, err := store.FindTransaction(ctx, tc.id, "")
		if err != nil || result.Kind != tc.kind || result.Status != tc.status || result.ExternalTransactionID != tc.external || result.WalletID != w.ID() {
			t.Fatal("Projeção inválida", tc.kind, err)
		}
		if tc.external != "" {
			external, err := store.FindExternal(ctx, "provider-a", tc.external)
			if err != nil || external != result {
				t.Fatal("Rotas consultaram estados diferentes", err)
			}
		}
		if _, err := store.FindTransaction(ctx, tc.id, "provider-b"); !errors.Is(err, query.ErrNotFound) {
			t.Fatal("Transação vazou entre provedores", err)
		}
	}
	if _, err := store.FindTransaction(ctx, "opening:"+w.ID(), "provider-a"); !errors.Is(err, query.ErrNotFound) {
		t.Fatal("Provedor enxergou abertura", err)
	}
	if _, err := store.FindExternal(ctx, "provider-b", "bet"); !errors.Is(err, query.ErrNotFound) {
		t.Fatal("Busca ignorou par provedor/ID", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 8, 5, 13})
	page, err := store.ListLedger(ctx, w.ID(), query.Cursor{}, 2)
	if err != nil || len(page.Entries) != 2 || page.Entries[0].Kind != "OPENING" || page.Entries[1].Kind != wager.BetKind || page.NextCursor == "" || page.SnapshotVersion != 5 {
		t.Fatal("Primeira página incorreta", err)
	}
	// Um novo crédito é confirmado entre páginas, sem contaminar o extrato iniciado.
	if _, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "new-win", "new-win-key", "1.00", "")); err != nil {
		t.Fatal(err)
	}
	entries := append([]query.LedgerEntry{}, page.Entries...)
	for page.NextCursor != "" {
		cursor, err := query.DecodeCursor(page.NextCursor, w.ID())
		if err != nil {
			t.Fatal(err)
		}
		page, err = store.ListLedger(ctx, w.ID(), cursor, 2)
		if err != nil || page.SnapshotVersion != 5 {
			t.Fatal(err)
		}
		entries = append(entries, page.Entries...)
	}
	if len(entries) != 5 {
		t.Fatal("Paginação duplicou ou incluiu crédito novo", len(entries))
	}
	for i, e := range entries {
		if e.WalletVersion != int64(i+1) || e.WalletID != w.ID() {
			t.Fatal("Ordem financeira instável")
		}
	}
	full, err := store.ListLedger(ctx, w.ID(), query.Cursor{}, 100)
	if err != nil || len(full.Entries) != 6 || full.NextCursor != "" || full.SnapshotVersion != 6 {
		t.Fatal("Nova navegação não incluiu crédito", err)
	}
	result, err := store.Reconcile(ctx, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	audit, err := query.BuildReconciliation(result)
	if err != nil || !audit.Consistent || audit.CheckedEntries != 6 || audit.CalculatedBalance.Amount != "101.00" {
		t.Fatal("Reconciliação incorreta", audit, err)
	}
	historical, err := store.FindTransaction(ctx, bet.ID(), "provider-a")
	if err != nil || historical.Balance.MinorUnits() != 7500 {
		t.Fatal("Consulta substituiu saldo original", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 9, 6, 15})
}

func TestEmptyLedgerMissingWalletAndInjectedDivergenceIntegration(t *testing.T) {
	ctx, pool := rollbackTestDatabase(t)
	w := repositoryTestWallet(t, "empty-ledger", "player", "0.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	s := NewReadStore(pool)
	page, err := s.ListLedger(ctx, w.ID(), query.Cursor{}, 50)
	if err != nil || page.Entries == nil || len(page.Entries) != 0 || page.NextCursor != "" {
		t.Fatal(err)
	}
	snapshot, err := s.Reconcile(ctx, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	result, err := query.BuildReconciliation(snapshot)
	if err != nil || !result.Consistent || result.CheckedEntries != 0 {
		t.Fatal(err)
	}
	if _, err := s.ListLedger(ctx, "absent", query.Cursor{}, 50); !errors.Is(err, wallet.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(ctx, "absent"); !errors.Is(err, wallet.ErrNotFound) {
		t.Fatal(err)
	}
	// Corrupção é injetada somente no schema descartável do teste para testar auditoria.
	if _, err := pool.Exec(ctx, `BEGIN; ALTER TABLE wallets DISABLE TRIGGER wallet_movement; UPDATE wallets SET balance_cents=1; ALTER TABLE wallets ENABLE TRIGGER wallet_movement; COMMIT;`); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.Reconcile(ctx, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	result, err = query.BuildReconciliation(snapshot)
	if err != nil || result.Consistent || result.Difference.Amount != "0.01" {
		t.Fatal(result, err)
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 1 || loaded.Version() != 1 {
		t.Fatal("Reconciliação corrigiu carteira", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 0, 0, 0})
}

func TestReconciliationIsConsistentWithConcurrentWritersIntegration(t *testing.T) {
	ctx, pool := rollbackTestDatabase(t)
	w := repositoryTestWallet(t, "concurrent-audit", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	s := NewReadStore(pool)
	var writers sync.WaitGroup
	failures := make(chan error, 1)
	writers.Go(func() {
		for i := range 30 {
			bet := pendingTestBet(t, w, "bet-"+string(rune('A'+i)), "bet-key-"+string(rune('A'+i)), "1.00")
			if _, _, err := NewBetStore(pool).ProcessBet(ctx, bet); err != nil {
				failures <- err
				return
			}
		}
		failures <- nil
	})
	for range 60 {
		snapshot, err := s.Reconcile(ctx, w.ID())
		if err != nil {
			t.Fatal(err)
		}
		result, err := query.BuildReconciliation(snapshot)
		if err != nil || !result.Consistent {
			t.Fatal("Saldo e ledger vieram de snapshots diferentes", result, err)
		}
	}
	writers.Wait()
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	final, err := s.Reconcile(ctx, w.ID())
	if err != nil || final.CheckedEntries != 31 || final.TotalMinorUnits != "7000" {
		t.Fatal(final, err)
	}
}

func TestReconciliationAggregateDoesNotOverflowInt64Integration(t *testing.T) {
	ctx, pool := rollbackTestDatabase(t)
	w := repositoryTestWallet(t, "wide-audit", "player", "92233720368547758.07", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "bet", "bet-key", "92233720368547758.07")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "win", "win-key", "92233720368547758.07", "")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewReadStore(pool).Reconcile(ctx, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	result, err := query.BuildReconciliation(snapshot)
	if err != nil || !result.Consistent || result.CalculatedBalance.Amount != "92233720368547758.07" || result.CheckedEntries != 3 {
		t.Fatal(result, err)
	}
}
