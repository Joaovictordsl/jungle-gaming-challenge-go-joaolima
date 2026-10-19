//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func winTestDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, pool := betTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000004_win.up.sql")
	return ctx, pool
}
func pendingTestWin(t *testing.T, w wallet.Wallet, external, key, amount, reference string) wager.Win {
	t.Helper()
	base := pendingTestBet(t, w, external, key, amount)
	win, err := wager.NewWin(rand.Text(), wager.WinRequest{BetRequest: base.Request(), ReferenceExternalTransactionID: reference}, base.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	return win
}

func TestWinStoreCreditReplayAndBetConflictsIntegration(t *testing.T) {
	ctx, pool := winTestDatabase(t)
	w := repositoryTestWallet(t, "win-wallet", "win-player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	store := NewWinStore(pool)
	first := pendingTestWin(t, w, "win-1", "key-1", "25.00", "")
	result, replay, err := store.ProcessWin(ctx, first)
	if err != nil || replay || result.Status() != wager.Processed || result.Balance().MinorUnits() != 12500 || result.WalletVersion() != 2 {
		t.Fatal("Crédito incorreto", err)
	}
	bet := pendingTestBet(t, w, "bet-after", "bet-after-key", "80.00")
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, bet); err != nil {
		t.Fatal(err)
	}
	repeated, replay, err := NewWinStore(pool).ProcessWin(ctx, first)
	if err != nil || !replay || repeated != result {
		t.Fatal("Replay não preservou o resultado original", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 3, 6})
	for _, tc := range []struct {
		external, key string
		cause         error
	}{
		{"another-id", "key-1", wager.ErrIdempotencyConflict}, {"win-1", "another-key", wager.ErrExternalConflict},
	} {
		if _, _, err := store.ProcessWin(ctx, pendingTestWin(t, w, tc.external, tc.key, "25.00", "")); !errors.Is(err, tc.cause) {
			t.Fatal("Conflito não identificado", err)
		}
	}
	// BET e WIN compartilham o espaço de idempotência por provedor.
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "win-1", "key-1", "25.00")); !errors.Is(err, wager.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, _, err := store.ProcessWin(ctx, pendingTestWin(t, w, "bet-after", "bet-after-key", "80.00", "")); !errors.Is(err, wager.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
}

func TestWinReferenceRecoveryAndSingleCreditIntegration(t *testing.T) {
	ctx, pool := winTestDatabase(t)
	w := repositoryTestWallet(t, "pending-win", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	pending := pendingTestWin(t, w, "win-before-bet", "win-key", "25.00", "bet-later")
	accepted, replay, err := NewWinStore(pool).ProcessWin(ctx, pending)
	if err != nil || replay || accepted.Status() != wager.PendingReference {
		t.Fatal("WIN não aguardou referência", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 1, 3})
	store := NewWinStore(pool)
	store.now = func() time.Time { return pending.CreatedAt().Add(2 * time.Second) }
	worked, err := store.RetryDueReference(ctx)
	if err != nil || !worked {
		t.Fatal("Retry da pendência não ocorreu", err)
	}
	var attempts int
	var next time.Time
	if err := pool.QueryRow(ctx, `SELECT reference_attempts,next_reference_attempt_at FROM wager_transactions WHERE id=$1`, accepted.ID()).Scan(&attempts, &next); err != nil || attempts != 1 || !next.Equal(pending.CreatedAt().Add(4*time.Second)) {
		t.Fatal("Backoff não persistido", err)
	}
	bet := pendingTestBet(t, w, "bet-later", "bet-key", "80.00")
	processedBet, _, err := NewBetStore(pool).ProcessBet(ctx, bet)
	if err != nil {
		t.Fatal(err)
	}
	// Duas instâncias assumem a mesma pendência; SKIP LOCKED impede crédito duplo.
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		independent := NewWinStore(pool)
		independent.now = func() time.Time { return pending.CreatedAt().Add(5 * time.Second) }
		workers.Go(func() { <-start; _, err := independent.RetryDueReference(ctx); results <- err })
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	resolved, replay, err := NewWinStore(pool).ProcessWin(ctx, pending)
	if err != nil || !replay || resolved.Status() != wager.Processed || resolved.ReferenceID() != processedBet.ID() || resolved.Balance().MinorUnits() != 4500 || resolved.WalletVersion() != 3 {
		t.Fatal("Referência retomada não confirmou crédito exato", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 3, 7})
	// Terminal imutável mesmo tendo sido uma pendência anteriormente.
	if _, err := pool.Exec(ctx, `UPDATE wager_transactions SET status='PENDING_REFERENCE' WHERE id=$1`, resolved.ID()); err == nil {
		t.Fatal("Terminal voltou a pendente")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM wager_transactions WHERE id=$1`, resolved.ID()); err == nil {
		t.Fatal("Histórico removido")
	}
}

func TestWinReferenceRejectionsAndOverflowIntegration(t *testing.T) {
	ctx, pool := winTestDatabase(t)
	w := repositoryTestWallet(t, "references", "player", "100.00", "BRL")
	max := repositoryTestWallet(t, "max-win", "max-player", "92233720368547758.07", "BRL")
	for _, current := range []wallet.Wallet{w, max} {
		if err := NewOpeningStore(pool).SaveOpening(ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	bet := pendingTestBet(t, w, "reference", "ref-key", "80.00")
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, bet); err != nil {
		t.Fatal(err)
	}
	invalid := pendingTestWin(t, w, "mismatch", "mismatch-key", "25.00", "reference")
	r := invalid.Request()
	r.RoundID = "another-round"
	invalid, _ = wager.NewWin(invalid.ID(), r, invalid.CreatedAt())
	rejected, _, err := NewWinStore(pool).ProcessWin(ctx, invalid)
	if err != nil || rejected.FailureCode() != wager.ReferenceMismatch {
		t.Fatal("Rodada incompatível não rejeitada", err)
	}
	rejectedBet := pendingTestBet(t, w, "rejected-bet", "rejected-bet-key", "80.00")
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, rejectedBet); err != nil {
		t.Fatal(err)
	}
	invalid = pendingTestWin(t, w, "rejected-ref", "rejected-ref-key", "25.00", "rejected-bet")
	rejected, _, err = NewWinStore(pool).ProcessWin(ctx, invalid)
	if err != nil || rejected.FailureCode() != wager.ReferenceRejected {
		t.Fatal("Aposta rejeitada autorizou WIN", err)
	}
	overflow := pendingTestWin(t, max, "overflow", "overflow-key", "0.01", "")
	rejected, _, err = NewWinStore(pool).ProcessWin(ctx, overflow)
	if err != nil || rejected.Status() != wager.Rejected || rejected.FailureCode() != wager.BalanceOverflow || rejected.Balance() != max.Balance() || rejected.WalletVersion() != 1 {
		t.Fatal("Overflow alterou saldo", err)
	}
	again, replay, err := NewWinStore(pool).ProcessWin(ctx, overflow)
	if err != nil || !replay || again != rejected {
		t.Fatal("Overflow não persistiu rejeição", err)
	}
	assertOpeningCounts(t, ctx, pool, max.ID(), [4]int{1, 2, 1, 3})
}

func TestWinReferenceAttemptsAndTTLIntegration(t *testing.T) {
	for _, byTTL := range []bool{false, true} {
		t.Run(map[bool]string{false: "limite de tentativas", true: "TTL"}[byTTL], func(t *testing.T) {
			ctx, pool := winTestDatabase(t)
			w := repositoryTestWallet(t, "expire-win", "player", "0.00", "BRL")
			if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
				t.Fatal(err)
			}
			pending := pendingTestWin(t, w, "win", "win-key", "1.00", "missing-bet")
			if _, _, err := NewWinStore(pool).ProcessWin(ctx, pending); err != nil {
				t.Fatal(err)
			}
			store := NewWinStore(pool)
			wantAttempts := referenceMaxAttempts
			if byTTL {
				wantAttempts = 1
			}
			for i := 0; i < wantAttempts; i++ {
				var next time.Time
				if err := pool.QueryRow(ctx, `SELECT next_reference_attempt_at FROM wager_transactions WHERE id=$1`, pending.ID()).Scan(&next); err != nil {
					t.Fatal(err)
				}
				if byTTL {
					next = pending.CreatedAt().Add(referenceTTL + time.Second)
				}
				store.now = func() time.Time { return next }
				if worked, err := store.RetryDueReference(ctx); err != nil || !worked {
					t.Fatal("Pendência não reprocessada", err)
				}
			}
			result, replay, err := NewWinStore(pool).ProcessWin(ctx, pending)
			if err != nil || !replay || result.Status() != wager.Rejected || result.FailureCode() != wager.ReferenceNotFound {
				t.Fatal("Esgotamento não auditado", err)
			}
			assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 0, 2})
			var attempts int
			if err := pool.QueryRow(ctx, `SELECT reference_attempts FROM wager_transactions WHERE id=$1`, pending.ID()).Scan(&attempts); err != nil || attempts != wantAttempts {
				t.Fatal(err)
			}
		})
	}
}

func TestWinRollbackAndMigrationCycleIntegration(t *testing.T) {
	ctx, pool := winTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000004_win.down.sql")
	runOpeningMigration(t, ctx, pool, "000004_win.up.sql")
	w := repositoryTestWallet(t, "rollback-win", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT fail_win_event CHECK
  (event_type <> 'WalletBalanceChanged' OR transaction_id LIKE 'opening:%')`); err != nil {
		t.Fatal(err)
	}
	pending := pendingTestWin(t, w, "win", "win-key", "25.00", "")
	if _, _, err := NewWinStore(pool).ProcessWin(ctx, pending); err == nil {
		t.Fatal("Falha injetada não abortou WIN")
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded != w {
		t.Fatal("Crédito parcial confirmado", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT fail_win_event`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewWinStore(pool).ProcessWin(ctx, pending); err != nil {
		t.Fatal("Retry não pôde confirmar WIN", err)
	}
	// Down deliberado recusa apagar WIN confirmado; a transação falha sem mudar schema.
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000004_win.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, string(data)); err == nil {
		t.Fatal("Down apagou histórico WIN")
	}
	_, _ = connection.Exec(ctx, "ROLLBACK")
	connection.Release()
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 2, 4})
}
