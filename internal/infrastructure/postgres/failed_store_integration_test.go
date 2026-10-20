//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	appwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

func failedTestDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, pool := outboxDatabase(t)
	runOpeningMigration(t, ctx, pool, "000011_financial_failed.up.sql")
	return ctx, pool
}

func TestFinancialFailedAllKindsReplayAndProtectHistoryIntegration(t *testing.T) {
	ctx, pool := failedTestDatabase(t)
	for _, kind := range []string{wager.BetKind, wager.WinKind, wager.LossKind, wager.RefundKind, wager.RollbackKind} {
		t.Run(kind, func(t *testing.T) {
			w := repositoryTestWallet(t, "failed-"+kind, "player-"+kind, "100.00", "BRL")
			if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
				t.Fatal(err)
			}
			// Referência válida permite chegar à gravação dos efeitos para REFUND/ROLLBACK.
			if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "original-"+kind, "original-key-"+kind, "10.00")); err != nil {
				t.Fatal(err)
			}
			e := inboxEnvelope(t, w, "msg-"+kind, "failed-"+kind, "failed-key-"+kind, "10.00")
			e.Command.Kind = kind
			if kind == wager.LossKind {
				e.Command.Money, _ = money.New("0.00", "BRL")
			}
			if kind == wager.RefundKind || kind == wager.RollbackKind {
				e.Command.ReferenceExternalTransactionID = "original-" + kind
			}
			// Falha depois de inserir a transação e, quando aplicável, movimentar
			// saldo/ledger. O savepoint precisa desfazer todos esses efeitos.
			if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT failed_injection CHECK(event_type='never') NOT VALID`); err != nil {
				t.Fatal(err)
			}
			service := inboxService(pool)
			if _, _, err := service.Execute(ctx, "consumer", e, false); err == nil {
				t.Fatal("falha transitória ignorada")
			}
			first, replay, err := service.Execute(ctx, "consumer", e, true)
			if message.PermanentCode(err) != "attempts_exhausted" || replay || first.FinancialReplay || first.Status != wager.Failed || first.FailureCode != wager.InfrastructureRetriesExhausted || first.Balance.MinorUnits() != 9000 || first.WalletVersion != 2 {
				t.Fatalf("falha sem snapshot correto: %+v %v", first, err)
			}
			if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT failed_injection`); err != nil {
				t.Fatal(err)
			}
			if _, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "later-"+kind, "later-key-"+kind, "1.00", "")); err != nil {
				t.Fatal(err)
			}
			// Um serviço novo representa a perda de todo estado em memória.
			again, replay, err := inboxService(pool).Execute(ctx, "consumer", e, false)
			if !replay || message.PermanentCode(err) != "attempts_exhausted" || again != first {
				t.Fatal("inbox perdeu snapshot terminal", err)
			}
			fresh := e
			fresh.MessageID += "-new"
			again, replay, err = inboxService(pool).Execute(ctx, "consumer", fresh, false)
			if replay || !again.FinancialReplay || again.TransactionID != first.TransactionID || message.PermanentCode(err) != "attempts_exhausted" {
				t.Fatal("outro messageId reaplicou falha", err)
			}
			stores := inboxStores{bet: NewBetStore(pool), loss: NewLossStore(pool), credit: NewWinStore(pool)}
			direct, err := appwager.NewDispatcher(inboxIDs{}, inboxClock{}).Execute(ctx, stores, e.Command)
			if err != nil || !direct.FinancialReplay || direct.TransactionID != first.TransactionID || direct.Status != wager.Failed || direct.Balance != first.Balance {
				t.Fatalf("replay dos stores HTTP inválido: %+v %v", direct, err)
			}
			changed := e.Command
			changed.RoundID = "changed"
			if _, err := appwager.NewDispatcher(inboxIDs{}, inboxClock{}).Execute(ctx, stores, changed); !errors.Is(err, wager.ErrIdempotencyConflict) {
				t.Fatal("FAILED perdeu conflito de payload", err)
			}
			changed = e.Command
			changed.IdempotencyKey += "-other"
			if _, err := appwager.NewDispatcher(inboxIDs{}, inboxClock{}).Execute(ctx, stores, changed); !errors.Is(err, wager.ErrExternalConflict) {
				t.Fatal("FAILED perdeu ID externo", err)
			}
			assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 4, 3, 6})
			current, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
			if err != nil || current.Balance().MinorUnits() != 9100 || current.Version() != 3 {
				t.Fatal("falha alterou carteira", err)
			}
			projection, err := NewReadStore(pool).FindExternal(ctx, e.Command.ProviderID, e.Command.ExternalTransactionID)
			if err != nil || projection.Status != wager.Failed || projection.TransactionID != first.TransactionID || projection.FailureCode != wager.InfrastructureRetriesExhausted {
				t.Fatal("consulta perdeu FAILED", err)
			}
			for _, sql := range []string{
				`UPDATE wager_transactions SET status='PROCESSED' WHERE id=$1`,
				`DELETE FROM wager_transactions WHERE id=$1`,
				`INSERT INTO wallet_ledger_entries(id,wallet_id,transaction_id,direction,currency,amount_cents,balance_before_cents,balance_after_cents,created_at) SELECT 'forged-'||id,wallet_id,id,'DEBIT',currency,100,9000,8900,updated_at FROM wager_transactions WHERE id=$1`,
				`INSERT INTO outbox_events(id,transaction_id,aggregate_id,event_type,payload,occurred_at,next_attempt_at) SELECT 'forged-'||id,id,wallet_id,'WagerTransactionRejected','{}',updated_at,updated_at FROM wager_transactions WHERE id=$1`,
			} {
				if _, err := pool.Exec(ctx, sql, first.TransactionID); err == nil {
					t.Fatal("banco permitiu alterar/forjar efeitos de FAILED")
				}
			}
		})
	}
}

func TestFinancialFailedConcurrentTerminalClaimsIntegration(t *testing.T) {
	ctx, pool := failedTestDatabase(t)
	w := repositoryTestWallet(t, "failed-concurrent", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	e := inboxEnvelope(t, w, "msg", "external", "key", "80.00")
	// Outra pool reproduz a coordenação exclusivamente SQL entre instâncias.
	other, err := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	results := make(chan appwager.Result, 12)
	failures := make(chan error, 12)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			<-start
			selected := pool
			if i%2 == 1 {
				selected = other
			}
			tx, err := selected.Begin(ctx)
			if err != nil {
				failures <- err
				return
			}
			defer rollbackFinancial(tx)
			r, err := recordFailedTransaction(ctx, tx, e.Command, time.Now().UTC().Truncate(time.Microsecond))
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- r
			failures <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	originals := 0
	for r := range results {
		if id == "" {
			id = r.TransactionID
		}
		if r.TransactionID != id || r.Status != wager.Failed {
			t.Fatal("auditorias concorrentes divergiram")
		}
		if !r.FinancialReplay {
			originals++
		}
	}
	if originals != 1 {
		t.Fatal("mais de uma falha financeira confirmada")
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 1, 2})
}

func TestFinancialFailedMigrationCycleAndAuditGuardIntegration(t *testing.T) {
	ctx, pool := failedTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000011_financial_failed.down.sql")
	runOpeningMigration(t, ctx, pool, "000011_financial_failed.up.sql")
	w := repositoryTestWallet(t, "failed-migration", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	e := inboxEnvelope(t, w, "msg", "ext", "key", "1.00")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordFailedTransaction(ctx, tx, e.Command, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		rollbackFinancial(tx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../migrations/000011_financial_failed.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, string(data)); err == nil {
		t.Fatal("down apagou histórico FAILED")
	}
	if _, err := conn.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
}

func TestFinancialFailedAuditPreservesPreviouslyCommittedResultsIntegration(t *testing.T) {
	ctx, pool := failedTestDatabase(t)
	w := repositoryTestWallet(t, "failed-winner", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	stores := inboxStores{bet: NewBetStore(pool), loss: NewLossStore(pool), credit: NewWinStore(pool)}
	dispatch := appwager.NewDispatcher(inboxIDs{}, inboxClock{})
	for _, tc := range []struct{ id, kind, amount, reference, status string }{
		{"processed", wager.BetKind, "1.00", "", wager.Processed},
		{"rejected", wager.BetKind, "200.00", "", wager.Rejected},
		{"pending", wager.WinKind, "1.00", "missing", wager.PendingReference},
	} {
		e := inboxEnvelope(t, w, tc.id, tc.id, tc.id+"-key", tc.amount)
		e.Command.Kind, e.Command.ReferenceExternalTransactionID = tc.kind, tc.reference
		first, err := dispatch.Execute(ctx, stores, e.Command)
		if err != nil || first.Status != tc.status {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		audit, err := recordFailedTransaction(ctx, tx, e.Command, time.Now().UTC().Truncate(time.Microsecond))
		if err != nil {
			rollbackFinancial(tx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if !audit.FinancialReplay || audit.TransactionID != first.TransactionID || audit.Status != first.Status || audit.Balance != first.Balance {
			t.Fatal("auditoria sobrescreveu resultado confirmado")
		}
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 4, 2, 6})
}

func TestFinancialFailedReferenceRejectsImmediateAndDurableDependentsIntegration(t *testing.T) {
	ctx, pool := failedTestDatabase(t)
	w := repositoryTestWallet(t, "failed-reference", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	stores := inboxStores{bet: NewBetStore(pool), loss: NewLossStore(pool), credit: NewWinStore(pool)}
	dispatch := appwager.NewDispatcher(inboxIDs{}, inboxClock{})
	for _, kind := range []string{wager.WinKind, wager.RefundKind, wager.RollbackKind} {
		e := inboxEnvelope(t, w, kind, "pending-"+kind, "pending-key-"+kind, "80.00")
		e.Command.Kind, e.Command.ReferenceExternalTransactionID = kind, "failed-bet"
		if r, err := dispatch.Execute(ctx, stores, e.Command); err != nil || r.Status != wager.PendingReference {
			t.Fatal(err)
		}
	}
	e := inboxEnvelope(t, w, "original", "failed-bet", "failed-bet-key", "80.00")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordFailedTransaction(ctx, tx, e.Command, time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		rollbackFinancial(tx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	refs := NewWinStore(pool)
	refs.now = func() time.Time { return time.Now().UTC().Add(2 * time.Second) }
	for range 3 {
		if worked, err := refs.RetryDueReference(ctx); err != nil || !worked {
			t.Fatal("dependência FAILED não retomada", err)
		}
	}
	for _, kind := range []string{wager.WinKind, wager.RefundKind, wager.RollbackKind} {
		pending, err := NewReadStore(pool).FindExternal(ctx, e.Command.ProviderID, "pending-"+kind)
		if err != nil || pending.Status != wager.Rejected || pending.FailureCode != wager.ReferenceRejected {
			t.Fatal("dependência durável não rejeitada", err)
		}
		immediate := e.Command
		immediate.Kind, immediate.ExternalTransactionID, immediate.IdempotencyKey, immediate.ReferenceExternalTransactionID = kind, "immediate-"+kind, "immediate-key-"+kind, "failed-bet"
		if r, err := dispatch.Execute(ctx, stores, immediate); err != nil || r.Status != wager.Rejected || r.FailureCode != wager.ReferenceRejected {
			t.Fatal("dependência imediata não rejeitada", err)
		}
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 8, 1, 11})
}
