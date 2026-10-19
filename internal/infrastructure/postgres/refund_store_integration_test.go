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

func refundTestDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, pool := lossTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000006_refund.up.sql")
	return ctx, pool
}
func pendingTestRefund(t *testing.T, w wallet.Wallet, external, key, amount, reference string) wager.Refund {
	t.Helper()
	win := pendingTestWin(t, w, external, key, amount, reference)
	r, err := wager.NewRefund(rand.Text(), wager.RefundRequest{WinRequest: win.Request()}, win.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRefundFullCreditAndDurableReplayIntegration(t *testing.T) {
	ctx, pool := refundTestDatabase(t)
	w := repositoryTestWallet(t, "refund-wallet", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "bet", "bet-key", "80.00")); err != nil {
		t.Fatal(err)
	}
	pending := pendingTestRefund(t, w, "refund", "refund-key", "80.00", "bet")
	first, replay, err := NewWinStore(pool).ProcessRefund(ctx, pending)
	if err != nil || replay || first.Status() != wager.Processed || first.Kind() != wager.RefundKind || first.Balance().MinorUnits() != 10000 || first.WalletVersion() != 3 {
		t.Fatal("Devolução integral falhou", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 3, 3, 6})
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "next-bet", "next-key", "10.00")); err != nil {
		t.Fatal(err)
	}
	again, replay, err := NewWinStore(pool).ProcessRefund(ctx, pending)
	if err != nil || !replay || again != first {
		t.Fatal("Replay não preservou resultado original", err)
	}
	duplicate, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, "another-refund", "another-key", "80.00", "bet"))
	if err != nil || duplicate.FailureCode() != wager.AlreadyReversed || duplicate.Balance().MinorUnits() != 9000 || duplicate.WalletVersion() != 4 {
		t.Fatal("Segunda devolução não rejeitada", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 5, 4, 9})
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 9000 || loaded.Version() != 4 {
		t.Fatal("Crédito duplicado", err)
	}
	for _, tc := range []struct {
		external, key, amount, ref string
		cause                      error
	}{
		{"refund", "refund-key", "79.00", "bet", wager.ErrIdempotencyConflict},
		{"refund", "refund-key", "80.00", "next-bet", wager.ErrIdempotencyConflict},
		{"refund", "new-key", "80.00", "bet", wager.ErrExternalConflict},
	} {
		if _, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, tc.external, tc.key, tc.amount, tc.ref)); !errors.Is(err, tc.cause) {
			t.Fatal(err)
		}
	}
	if _, _, err := NewWinStore(pool).ProcessWin(ctx, pending); !errors.Is(err, wager.ErrInvalidState) {
		t.Fatal("REFUND aceito no store WIN", err)
	}
	if _, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestWin(t, w, "win", "win-key", "1.00", "")); !errors.Is(err, wager.ErrInvalidState) {
		t.Fatal("WIN aceito no store REFUND", err)
	}
}

func TestRefundInvalidReferencesIntegration(t *testing.T) {
	ctx, pool := refundTestDatabase(t)
	w := repositoryTestWallet(t, "invalid-refund", "player", "100.00", "BRL")
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
	for _, tc := range []struct{ external, amount, ref, code string }{
		{"partial", "79.00", "bet", wager.ReferenceAmountMismatch},
		{"over", "81.00", "bet", wager.ReferenceAmountMismatch},
		{"rejected", "80.00", "rejected-bet", wager.ReferenceRejected},
		{"wrong-kind", "80.00", "loss", wager.ReferenceMismatch},
	} {
		result, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, tc.external, "refund-"+tc.external+"-key", tc.amount, tc.ref))
		if err != nil || result.Status() != wager.Rejected || result.FailureCode() != tc.code || result.Balance().MinorUnits() != 2000 || result.WalletVersion() != 2 {
			t.Fatal(tc.external, err)
		}
	}
	// A aposta de outro provedor é invisível para a resolução da referência.
	p := pendingTestRefund(t, w, "isolated", "isolated-key", "80.00", "bet")
	r := p.Request()
	r.ProviderID = "provider-b"
	isolated, _ := wager.NewRefund(p.ID(), wager.RefundRequest{WinRequest: r}, p.CreatedAt())
	result, _, err := NewWinStore(pool).ProcessRefund(ctx, isolated)
	if err != nil || result.Status() != wager.PendingReference {
		t.Fatal("Referência vazou entre provedores", err)
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 2000 || loaded.Version() != 2 {
		t.Fatal(err)
	}
}

func TestRefundPendingReferencesCompeteAndExpireIntegration(t *testing.T) {
	ctx, pool := refundTestDatabase(t)
	w := repositoryTestWallet(t, "pending-refund", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	first := pendingTestRefund(t, w, "first", "first-key", "80.00", "future-bet")
	second := pendingTestRefund(t, w, "second", "second-key", "80.00", "future-bet")
	for _, p := range []wager.Refund{first, second} {
		result, _, err := NewWinStore(pool).ProcessRefund(ctx, p)
		if err != nil || result.Status() != wager.PendingReference {
			t.Fatal(err)
		}
	}
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "future-bet", "bet-key", "80.00")); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	failures := make(chan error, 2)
	for range 2 {
		workers.Go(func() {
			s := NewWinStore(pool)
			s.now = func() time.Time { return second.CreatedAt().Add(2 * time.Second) }
			_, err := s.RetryDueReference(ctx)
			failures <- err
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	processed, rejected := 0, 0
	for _, p := range []wager.Refund{first, second} {
		result, replay, err := NewWinStore(pool).ProcessRefund(ctx, p)
		if err != nil || !replay {
			t.Fatal(err)
		}
		switch result.Status() {
		case wager.Processed:
			processed++
		case wager.Rejected:
			if result.FailureCode() != wager.AlreadyReversed {
				t.Fatal(result.FailureCode())
			}
			rejected++
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatal("Workers creditaram referência mais de uma vez")
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 4, 3, 9})
	missing := pendingTestRefund(t, w, "missing", "missing-key", "1.00", "never-arrives")
	if _, _, err := NewWinStore(pool).ProcessRefund(ctx, missing); err != nil {
		t.Fatal(err)
	}
	s := NewWinStore(pool)
	s.now = func() time.Time { return missing.CreatedAt().Add(referenceTTL + 10*time.Second) }
	if worked, err := s.RetryDueReference(ctx); err != nil || !worked {
		t.Fatal(err)
	}
	expired, replay, err := NewWinStore(pool).ProcessRefund(ctx, missing)
	if err != nil || !replay || expired.FailureCode() != wager.ReferenceNotFound {
		t.Fatal("TTL não rejeitou pendência", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 5, 3, 11})
}

func TestRefundRollsBackEffectsIntegration(t *testing.T) {
	for _, injection := range []string{
		`ALTER TABLE outbox_events ADD CONSTRAINT fail_refund_event CHECK (event_type <> 'WalletBalanceChanged') NOT VALID`,
		`CREATE FUNCTION reject_refund_commit() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected commit failure' USING ERRCODE='23514'; END; $$; CREATE CONSTRAINT TRIGGER fail_refund_commit AFTER INSERT ON wager_transactions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.kind='REFUND') EXECUTE FUNCTION reject_refund_commit()`,
	} {
		t.Run(injection[:20], func(t *testing.T) {
			ctx, pool := refundTestDatabase(t)
			w := repositoryTestWallet(t, "atomic-refund", "player", "100.00", "BRL")
			if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
				t.Fatal(err)
			}
			if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "bet", "bet-key", "80.00")); err != nil {
				t.Fatal(err)
			}
			before, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, injection); err != nil {
				t.Fatal(err)
			}
			if _, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, "refund", "refund-key", "80.00", "bet")); err == nil {
				t.Fatal("Falha não abortou crédito")
			}
			assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 2, 4})
			after, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
			if err != nil || after != before {
				t.Fatal("Rollback alterou carteira", err)
			}
		})
	}
}

func TestRefundMigrationPreservesHistoryAndProtectsReversalIntegration(t *testing.T) {
	ctx, pool := lossTestDatabase(t)
	w := repositoryTestWallet(t, "migrated-refund", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	bet := pendingTestBet(t, w, "bet", "bet-key", "80.00")
	b, _, err := NewBetStore(pool).ProcessBet(ctx, bet)
	if err != nil {
		t.Fatal(err)
	}
	win := pendingTestWin(t, w, "win", "win-key", "1.00", "")
	prize, _, err := NewWinStore(pool).ProcessWin(ctx, win)
	if err != nil {
		t.Fatal(err)
	}
	loss := pendingTestLoss(t, w, "loss", "loss-key")
	lost, _, err := NewLossStore(pool).ProcessLoss(ctx, loss)
	if err != nil {
		t.Fatal(err)
	}
	runOpeningMigration(t, ctx, pool, "000006_refund.up.sql")
	runOpeningMigration(t, ctx, pool, "000006_refund.down.sql")
	runOpeningMigration(t, ctx, pool, "000006_refund.up.sql")
	br, replay, err := NewBetStore(pool).ProcessBet(ctx, bet)
	if err != nil || !replay || br != b {
		t.Fatal("Migration alterou BET", err)
	}
	wr, replay, err := NewWinStore(pool).ProcessWin(ctx, win)
	if err != nil || !replay || wr != prize {
		t.Fatal("Migration alterou WIN", err)
	}
	lr, replay, err := NewLossStore(pool).ProcessLoss(ctx, loss)
	if err != nil || !replay || lr != lost {
		t.Fatal("Migration alterou LOSS", err)
	}
	if _, _, err := NewWinStore(pool).ProcessRefund(ctx, pendingTestRefund(t, w, "refund", "refund-key", "80.00", "bet")); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE wager_transactions SET status='PENDING_REFERENCE' WHERE kind='REFUND'`,
		`DELETE FROM wager_transactions WHERE kind='REFUND'`,
		`INSERT INTO wager_transactions(id,origin,kind,wallet_id,player_id,currency,amount_cents,status,balance_after_cents,wallet_version,created_at,updated_at,provider_id,external_transaction_id,idempotency_key,payload_hash,round_id,game_id,balance_before_cents,reference_external_transaction_id,reference_transaction_id)
   SELECT 'duplicate',origin,kind,wallet_id,player_id,currency,amount_cents,status,balance_after_cents,wallet_version,created_at,updated_at,provider_id,'duplicate','duplicate-key',payload_hash,round_id,game_id,balance_before_cents,reference_external_transaction_id,reference_transaction_id FROM wager_transactions WHERE kind='REFUND'`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatal("Banco aceitou histórico inválido", statement)
		}
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000006_refund.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(data)); err == nil {
		t.Fatal("Down removeu histórico financeiro")
	}
	_, _ = conn.Exec(ctx, "ROLLBACK")
	conn.Release()
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 5, 4, 9})
}
