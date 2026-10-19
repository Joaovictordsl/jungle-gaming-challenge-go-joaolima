//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func rollbackTestDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, pool := refundTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000007_rollback.up.sql")
	return ctx, pool
}

// A aplicação valida a referência antes do crédito/débito; este teste tenta
// violar a regra por SQL, com ledger e eventos completos, para provar o guard do banco.
func TestRollbackDatabaseRejectsWrongDirectionIntegration(t *testing.T) {
	ctx, pool := rollbackTestDatabase(t)
	w := repositoryTestWallet(t, "wrong-direction", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	original, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "win", "win-key", "80.00", ""))
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingTestRollback(t, w, "invalid", "invalid-key", "80.00", "win")
	r := pending.Request()
	moved, err := current.Credit(r.Money, pending.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	forged, err := wager.RehydrateRollback(pending.ID(), wager.RollbackRequest{WinRequest: r}, wager.Processed, "", original.ID(), ledger.Credit, current.Balance(), moved.Balance(), moved.Version(), pending.CreatedAt(), moved.UpdatedAt())
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := forged.PayloadHash()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackFinancial(tx)
	_, err = tx.Exec(ctx, `INSERT INTO wager_transactions(id,origin,kind,wallet_id,player_id,currency,amount_cents,status,balance_after_cents,wallet_version,created_at,updated_at,provider_id,external_transaction_id,idempotency_key,payload_hash,round_id,game_id,balance_before_cents,reference_external_transaction_id,reference_transaction_id)
 VALUES($1,'EXTERNAL','ROLLBACK',$2,$3,'BRL',8000,'PROCESSED',$4,$5,$6,$6,'provider-a','invalid','invalid-key',$7,'round','game',$8,'win',$9)`, forged.ID(), w.ID(), w.PlayerID(), moved.Balance().MinorUnits(), moved.Version(), forged.UpdatedAt(), hash, current.Balance().MinorUnits(), original.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := saveReferencedEffects(ctx, tx, forged, current, moved, true); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("Banco permitiu creditar ao desfazer WIN")
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded != current {
		t.Fatal("Falha do guard alterou carteira", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 2, 4})
}
func pendingTestRollback(t *testing.T, w wallet.Wallet, external, key, amount, reference string) wager.Rollback {
	t.Helper()
	win := pendingTestWin(t, w, external, key, amount, reference)
	r, err := wager.NewRollback(rand.Text(), wager.RollbackRequest{WinRequest: win.Request()}, win.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestRollbackInvertsAllSupportedTypesIntegration(t *testing.T) {
	for _, kind := range []string{wager.BetKind, wager.WinKind, wager.RefundKind} {
		t.Run(kind, func(t *testing.T) {
			ctx, pool := rollbackTestDatabase(t)
			w := repositoryTestWallet(t, "rollback-wallet", "player", "100.00", "BRL")
			if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
				t.Fatal(err)
			}
			reference := "original"
			wantBalance := int64(10000)
			wantVersion := int64(3)
			wantDirection := "CREDIT"
			counts := [4]int{1, 3, 3, 6}
			switch kind {
			case wager.BetKind:
				if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, reference, "original-key", "80.00")); err != nil {
					t.Fatal(err)
				}
			case wager.WinKind:
				if _, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, reference, "original-key", "80.00", "")); err != nil {
					t.Fatal(err)
				}
				wantDirection = "DEBIT"
			case wager.RefundKind:
				if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "bet", "bet-key", "80.00")); err != nil {
					t.Fatal(err)
				}
				if _, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, reference, "original-key", "80.00", "bet")); err != nil {
					t.Fatal(err)
				}
				wantDirection = "DEBIT"
				wantBalance = 2000
				wantVersion = 4
				counts = [4]int{1, 4, 4, 8}
			}
			pending := pendingTestRollback(t, w, "rollback", "rollback-key", "80.00", reference)
			first, replay, err := NewWinStore(pool).ProcessRollback(ctx, pending)
			if err != nil || replay || first.Status() != wager.Processed || first.Balance().MinorUnits() != wantBalance || first.WalletVersion() != wantVersion || first.Direction() != wantDirection {
				t.Fatal("Estorno incorreto", err)
			}
			assertOpeningCounts(t, ctx, pool, w.ID(), counts)
			var direction string
			if err := pool.QueryRow(ctx, `SELECT direction FROM wallet_ledger_entries WHERE transaction_id=$1`, first.ID()).Scan(&direction); err != nil || direction != wantDirection {
				t.Fatal("Ledger com direção incorreta", err)
			}
			if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "next", "next-key", "10.00")); err != nil {
				t.Fatal(err)
			}
			repeated, replay, err := NewWinStore(pool).ProcessRollback(ctx, pending)
			if err != nil || !replay || repeated != first {
				t.Fatal("Replay alterou estorno", err)
			}
			duplicate, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, "duplicate", "duplicate-key", "80.00", reference))
			if err != nil || duplicate.FailureCode() != wager.AlreadyReversed {
				t.Fatal("Estorno duplicado", err)
			}
			if kind == wager.BetKind {
				r, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, "refund-after", "refund-after-key", "80.00", reference))
				if err != nil || r.FailureCode() != wager.AlreadyReversed {
					t.Fatal("REFUND após ROLLBACK duplicou devolução", err)
				}
			}
			if kind == wager.RefundKind {
				r, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, "refund-after", "refund-after-key", "80.00", "bet"))
				if err != nil || r.FailureCode() != wager.AlreadyReversed {
					t.Fatal("Estorno de REFUND reabriu BET", err)
				}
			}
			for _, tc := range []struct {
				external, key, amount, ref string
				cause                      error
			}{{"rollback", "rollback-key", "79.00", reference, wager.ErrIdempotencyConflict}, {"rollback", "rollback-key", "80.00", "next", wager.ErrIdempotencyConflict}, {"rollback", "other-key", "80.00", reference, wager.ErrExternalConflict}} {
				if _, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, tc.external, tc.key, tc.amount, tc.ref)); !errors.Is(err, tc.cause) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRollbackInsufficientFundsIsDurableIntegration(t *testing.T) {
	ctx, pool := rollbackTestDatabase(t)
	w := repositoryTestWallet(t, "insufficient-reversal", "player", "0.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "win", "win-key", "80.00", "")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "bet", "bet-key", "70.00")); err != nil {
		t.Fatal(err)
	}
	pending := pendingTestRollback(t, w, "rollback", "rollback-key", "80.00", "win")
	first, _, err := NewWinStore(pool).ProcessRollback(ctx, pending)
	if err != nil || first.FailureCode() != wager.ReversalInsufficientFunds || first.Balance().MinorUnits() != 1000 || first.WalletVersion() != 3 {
		t.Fatal("Rejeição de estorno incorreta", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 2, 5})
	if _, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "topup", "topup-key", "70.00", "")); err != nil {
		t.Fatal(err)
	}
	repeated, replay, err := NewWinStore(pool).ProcessRollback(ctx, pending)
	if err != nil || !replay || repeated != first {
		t.Fatal("Rejeição terminal foi reaplicada", err)
	}
	next, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, "new-attempt", "new-attempt-key", "80.00", "win"))
	if err != nil || next.Status() != wager.Processed || next.Balance().MinorUnits() != 0 {
		t.Fatal("Tentativa rejeitada reservou referência", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 5, 4, 9})
}

func TestRollbackPendingCreditReferenceAndExpiryIntegration(t *testing.T) {
	ctx, pool := rollbackTestDatabase(t)
	w := repositoryTestWallet(t, "pending-rollback", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	win := pendingTestWin(t, w, "win", "win-key", "80.00", "future-bet")
	if result, _, err := NewWinStore(pool).ProcessWin(ctx, win); err != nil || result.Status() != wager.PendingReference {
		t.Fatal(err)
	}
	pending := pendingTestRollback(t, w, "rollback", "rollback-key", "80.00", "win")
	waiting, _, err := NewWinStore(pool).ProcessRollback(ctx, pending)
	if err != nil || waiting.Status() != wager.PendingReference {
		t.Fatal("Referência pendente não aguardou", err)
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "future-bet", "bet-key", "10.00")); err != nil {
		t.Fatal(err)
	}
	s := NewWinStore(pool)
	s.now = func() time.Time { return pending.CreatedAt().Add(2 * time.Second) }
	for range 2 {
		if worked, err := s.RetryDueReference(ctx); err != nil || !worked {
			t.Fatal(err)
		}
	}
	completed, replay, err := NewWinStore(pool).ProcessRollback(ctx, pending)
	if err != nil || !replay || completed.Status() != wager.Processed || completed.Direction() != "DEBIT" || completed.Balance().MinorUnits() != 9000 {
		t.Fatal("Worker não desfez crédito", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 4, 4, 10})
	missing := pendingTestRollback(t, w, "missing", "missing-key", "1.00", "absent")
	if _, _, err := NewWinStore(pool).ProcessRollback(ctx, missing); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return missing.CreatedAt().Add(referenceTTL + 10*time.Second) }
	if worked, err := s.RetryDueReference(ctx); err != nil || !worked {
		t.Fatal(err)
	}
	expired, replay, err := NewWinStore(pool).ProcessRollback(ctx, missing)
	if err != nil || !replay || expired.FailureCode() != wager.ReferenceNotFound {
		t.Fatal(err)
	}
}

func TestRollbackInvalidReferencesAndRefundPolicyIntegration(t *testing.T) {
	ctx, pool := rollbackTestDatabase(t)
	w := repositoryTestWallet(t, "invalid-rollback", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "bet", "bet-key", "80.00")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "rejected-bet", "rejected-key", "80.00")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewLossStore(pool).ProcessLoss(ctx, pendingTestLoss(t, w, "loss", "loss-key")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ external, ref, amount, code string }{{"partial", "bet", "79.00", wager.ReferenceAmountMismatch}, {"rejected", "rejected-bet", "80.00", wager.ReferenceRejected}, {"bad-kind", "loss", "80.00", wager.ReferenceMismatch}} {
		result, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, tc.external, "rb-"+tc.external, tc.amount, tc.ref))
		if err != nil || result.FailureCode() != tc.code {
			t.Fatal(tc.external, err)
		}
	}
	if _, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, "refund", "refund-key", "80.00", "bet")); err != nil {
		t.Fatal(err)
	}
	result, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, "blocked", "blocked-key", "80.00", "bet"))
	if err != nil || result.FailureCode() != wager.AlreadyReversed {
		t.Fatal("ROLLBACK após REFUND duplicou devolução", err)
	}
	other, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, "of-refund", "of-refund-key", "80.00", "refund"))
	if err != nil || other.Status() != wager.Processed {
		t.Fatal(err)
	}
	invalid, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, "of-rollback", "of-rollback-key", "80.00", "of-refund"))
	if err != nil || invalid.FailureCode() != wager.ReferenceMismatch {
		t.Fatal("Rollback de rollback aceito", err)
	}
}

func TestRollbackEffectsAreAtomicAndHistoryIsProtectedIntegration(t *testing.T) {
	for _, injection := range []string{
		`ALTER TABLE outbox_events ADD CONSTRAINT fail_rollback_event CHECK(event_type<>'WalletBalanceChanged') NOT VALID`,
		`CREATE FUNCTION reject_rollback_commit() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected' USING ERRCODE='23514'; END; $$; CREATE CONSTRAINT TRIGGER fail_rollback_commit AFTER INSERT ON wager_transactions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN(NEW.kind='ROLLBACK') EXECUTE FUNCTION reject_rollback_commit()`,
	} {
		t.Run(injection[:20], func(t *testing.T) {
			ctx, pool := rollbackTestDatabase(t)
			w := repositoryTestWallet(t, "atomic-rollback", "player", "100.00", "BRL")
			if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
				t.Fatal(err)
			}
			if _, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "win", "win-key", "80.00", "")); err != nil {
				t.Fatal(err)
			}
			before, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, injection); err != nil {
				t.Fatal(err)
			}
			if _, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, "rollback", "rollback-key", "80.00", "win")); err == nil {
				t.Fatal("Falha não abortou débito")
			}
			after, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
			if err != nil || after != before {
				t.Fatal("Rollback SQL alterou carteira", err)
			}
			assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 2, 4})
		})
	}
}

func TestRollbackMigrationCycleAndTerminalGuardsIntegration(t *testing.T) {
	ctx, pool := refundTestDatabase(t)
	w := repositoryTestWallet(t, "migrated-rollback", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	bet := pendingTestBet(t, w, "bet", "bet-key", "80.00")
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, bet); err != nil {
		t.Fatal(err)
	}
	refund := pendingTestRefund(t, w, "refund", "refund-key", "80.00", "bet")
	original, _, err := NewWinStore(pool).ProcessRefund(ctx, refund)
	if err != nil {
		t.Fatal(err)
	}
	runOpeningMigration(t, ctx, pool, "000007_rollback.up.sql")
	runOpeningMigration(t, ctx, pool, "000007_rollback.down.sql")
	runOpeningMigration(t, ctx, pool, "000007_rollback.up.sql")
	repeated, replay, err := NewWinStore(pool).ProcessRefund(ctx, refund)
	if err != nil || !replay || repeated != original {
		t.Fatal("Migration alterou histórico anterior", err)
	}
	if _, _, err := NewWinStore(pool).ProcessRollback(ctx, pendingTestRollback(t, w, "rollback", "rollback-key", "80.00", "refund")); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE wager_transactions SET status='PENDING_REFERENCE' WHERE kind='ROLLBACK'`, `DELETE FROM wager_transactions WHERE kind='ROLLBACK'`, `UPDATE wallet_ledger_entries SET direction='CREDIT' WHERE transaction_id IN(SELECT id FROM wager_transactions WHERE kind='ROLLBACK')`} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatal("Banco aceitou alteração de histórico", query)
		}
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000007_rollback.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(data)); err == nil {
		t.Fatal("Down removeu histórico ROLLBACK")
	}
	_, _ = conn.Exec(ctx, "ROLLBACK")
	conn.Release()
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 4, 4, 8})
}
