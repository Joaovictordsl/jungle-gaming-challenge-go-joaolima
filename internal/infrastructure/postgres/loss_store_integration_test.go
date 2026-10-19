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

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func lossTestDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, pool := winTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000005_loss.up.sql")
	return ctx, pool
}
func pendingTestLoss(t *testing.T, w wallet.Wallet, external, key string) wager.Loss {
	t.Helper()
	zero, _ := money.Zero(w.Balance().Currency())
	r := wager.LossRequest{BetRequest: wager.BetRequest{ProviderID: "provider-a", ExternalTransactionID: external, IdempotencyKey: key, WalletID: w.ID(), PlayerID: w.PlayerID(), RoundID: "round", GameID: "game", Money: zero}}
	loss, err := wager.NewLoss(rand.Text(), r, time.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	return loss
}

func TestLossStorePreservesWalletAndDurableReplayIntegration(t *testing.T) {
	ctx, pool := lossTestDatabase(t)
	w := repositoryTestWallet(t, "loss-wallet", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	pending := pendingTestLoss(t, w, "loss-1", "loss-key")
	first, replay, err := NewLossStore(pool).ProcessLoss(ctx, pending)
	if err != nil || replay || first.Status() != wager.Processed || first.Balance() != w.Balance() || first.WalletVersion() != 1 {
		t.Fatal("LOSS não preservou saldo/versão", err)
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded != w {
		t.Fatal("LOSS alterou carteira ou seus timestamps", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 1, 3})
	bet := pendingTestBet(t, w, "bet-after-loss", "bet-key", "80.00")
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, bet); err != nil {
		t.Fatal(err)
	}
	repeated, replay, err := NewLossStore(pool).ProcessLoss(ctx, pending)
	if err != nil || !replay || repeated != first {
		t.Fatal("Replay de LOSS não devolveu resultado original", err)
	}
	// Operações distintas de LOSS podem compartilhar a mesma versão da carteira.
	for _, external := range []string{"loss-2", "loss-3"} {
		loss, replay, err := NewLossStore(pool).ProcessLoss(ctx, pendingTestLoss(t, w, external, external+"-key"))
		if err != nil || replay || loss.Balance().MinorUnits() != 2000 || loss.WalletVersion() != 2 {
			t.Fatal("LOSS exigiu nova versão ou alterou saldo", err)
		}
	}
	loaded, err = NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 2000 || loaded.Version() != 2 {
		t.Fatal(err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 5, 2, 7})
	// Mesmo espaço de idempotência entre tipos: uma chave de LOSS não vira BET/WIN.
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "loss-1", "loss-key", "1.00")); !errors.Is(err, wager.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, _, err := NewWinStore(pool).ProcessWin(ctx, pendingTestWin(t, w, "loss-1", "loss-key", "1.00", "")); !errors.Is(err, wager.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, _, err := NewLossStore(pool).ProcessLoss(ctx, pendingTestLoss(t, w, "bet-after-loss", "bet-key")); !errors.Is(err, wager.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, _, err := NewLossStore(pool).ProcessLoss(ctx, pendingTestLoss(t, w, "loss-1", "another-key")); !errors.Is(err, wager.ErrExternalConflict) {
		t.Fatal(err)
	}
	changed := pending.Request()
	changed.RoundID = "other-round"
	other, _ := wager.NewLoss(rand.Text(), changed, pending.CreatedAt())
	if _, _, err := NewLossStore(pool).ProcessLoss(ctx, other); !errors.Is(err, wager.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	// LOSS não é uma aposta e não pode autorizar um prêmio referenciado.
	invalidWin := pendingTestWin(t, w, "win-ref-loss", "win-ref-loss-key", "1.00", "loss-1")
	rejected, _, err := NewWinStore(pool).ProcessWin(ctx, invalidWin)
	if err != nil || rejected.FailureCode() != wager.ReferenceMismatch {
		t.Fatal("LOSS usado como referência de WIN", err)
	}
}

func TestLossConcurrentDuplicatesAndCancellationIntegration(t *testing.T) {
	ctx, pool := lossTestDatabase(t)
	w := repositoryTestWallet(t, "concurrent-loss", "player", "0.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	pending := pendingTestLoss(t, w, "duplicate", "duplicate-key")
	start := make(chan struct{})
	results := make(chan bool, 50)
	failures := make(chan error, 50)
	var workers sync.WaitGroup
	for range 50 {
		workers.Go(func() {
			<-start
			_, replay, err := NewLossStore(pool).ProcessLoss(ctx, pending)
			results <- replay
			failures <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	close(failures)
	originals := 0
	for replay := range results {
		if !replay {
			originals++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if originals != 1 {
		t.Fatal("Duplicatas geraram mais de uma transação")
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 0, 1})
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded != w {
		t.Fatal("Concorrência LOSS alterou carteira", err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, w.ID()); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, _, err := NewLossStore(pool).ProcessLoss(deadline, pendingTestLoss(t, w, "timeout", "timeout-key")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Lock não respeitou cancelamento", err)
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 0, 1})
}

func TestLossRollsBackOutboxAndCommitFailuresIntegration(t *testing.T) {
	for _, injection := range []string{
		`ALTER TABLE outbox_events ADD CONSTRAINT fail_loss_event CHECK (payload->'data'->>'kind' <> 'LOSS')`,
		`CREATE FUNCTION reject_loss_commit() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected commit failure' USING ERRCODE='23514'; END; $$;
   CREATE CONSTRAINT TRIGGER fail_loss_commit AFTER INSERT ON wager_transactions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.kind='LOSS') EXECUTE FUNCTION reject_loss_commit()`,
	} {
		t.Run(injection[:20], func(t *testing.T) {
			ctx, pool := lossTestDatabase(t)
			w := repositoryTestWallet(t, "rollback-loss", "player", "100.00", "BRL")
			if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, injection); err != nil {
				t.Fatal(err)
			}
			if _, _, err := NewLossStore(pool).ProcessLoss(ctx, pendingTestLoss(t, w, "loss", "key")); err == nil {
				t.Fatal("Falha não abortou LOSS")
			}
			assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
			loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
			if err != nil || loaded != w {
				t.Fatal("Rollback modificou carteira", err)
			}
		})
	}
}

func TestLossSchemaGuardsAndMigrationCycleIntegration(t *testing.T) {
	ctx, pool := lossTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000005_loss.down.sql")
	runOpeningMigration(t, ctx, pool, "000005_loss.up.sql")
	w := repositoryTestWallet(t, "protected-loss", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	loss, _, err := NewLossStore(pool).ProcessLoss(ctx, pendingTestLoss(t, w, "loss", "key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE wager_transactions SET status='PENDING' WHERE kind='LOSS'`,
		`DELETE FROM wager_transactions WHERE kind='LOSS'`,
		`UPDATE wallets SET version=version+1`,
		`INSERT INTO wager_transactions(id,origin,kind,wallet_id,player_id,currency,amount_cents,status,balance_after_cents,wallet_version,created_at,updated_at,provider_id,external_transaction_id,idempotency_key,payload_hash,round_id,game_id,balance_before_cents)
   SELECT 'incomplete-loss',origin,kind,wallet_id,player_id,currency,amount_cents,status,balance_after_cents,wallet_version,created_at,updated_at,provider_id,'incomplete','incomplete-key',payload_hash,round_id,game_id,balance_before_cents FROM wager_transactions WHERE kind='LOSS'`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatal("Banco aceitou operação inválida", statement)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO wallet_ledger_entries(id,wallet_id,transaction_id,direction,currency,amount_cents,balance_before_cents,balance_after_cents,created_at)
  VALUES ('forbidden',$1,$2,'DEBIT','BRL',0,10000,10000,$3)`, w.ID(), loss.ID(), loss.UpdatedAt()); err == nil {
		t.Fatal("Banco aceitou ledger de LOSS")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,transaction_id,aggregate_id,event_type,payload,occurred_at,next_attempt_at)
  SELECT 'bad-loss-event',transaction_id,aggregate_id,'WalletBalanceChanged',payload||jsonb_build_object('eventId','bad-loss-event','eventType','WalletBalanceChanged'),occurred_at,next_attempt_at
  FROM outbox_events WHERE transaction_id=$1`, loss.ID()); err == nil {
		t.Fatal("Banco aceitou WalletBalanceChanged para LOSS")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000005_loss.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, string(data)); err == nil {
		t.Fatal("Down removeu histórico LOSS")
	}
	_, _ = connection.Exec(ctx, "ROLLBACK")
	connection.Release()
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 1, 3})
}

func TestLossMigrationPreservesExistingBetAndWinIntegration(t *testing.T) {
	ctx, pool := winTestDatabase(t)
	w := repositoryTestWallet(t, "existing-financial-history", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	bet := pendingTestBet(t, w, "old-bet", "old-bet-key", "25.00")
	betResult, _, err := NewBetStore(pool).ProcessBet(ctx, bet)
	if err != nil {
		t.Fatal(err)
	}
	win := pendingTestWin(t, w, "old-win", "old-win-key", "10.00", "old-bet")
	winResult, _, err := NewWinStore(pool).ProcessWin(ctx, win)
	if err != nil {
		t.Fatal(err)
	}
	runOpeningMigration(t, ctx, pool, "000005_loss.up.sql")
	runOpeningMigration(t, ctx, pool, "000005_loss.down.sql")
	runOpeningMigration(t, ctx, pool, "000005_loss.up.sql")
	betReplay, replay, err := NewBetStore(pool).ProcessBet(ctx, bet)
	if err != nil || !replay || betReplay != betResult {
		t.Fatal("Migration alterou BET ou seu hash", err)
	}
	winReplay, replay, err := NewWinStore(pool).ProcessWin(ctx, win)
	if err != nil || !replay || winReplay != winResult {
		t.Fatal("Migration alterou WIN ou sua referência", err)
	}
	loss, _, err := NewLossStore(pool).ProcessLoss(ctx, pendingTestLoss(t, w, "new-loss", "new-loss-key"))
	if err != nil || loss.Balance().MinorUnits() != 8500 || loss.WalletVersion() != 3 {
		t.Fatal("LOSS alterou histórico migrado", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 4, 3, 7})
}
