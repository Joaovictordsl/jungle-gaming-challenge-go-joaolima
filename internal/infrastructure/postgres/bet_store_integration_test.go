//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func betTestDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, pool := openingStoreTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000003_bet.up.sql")
	return ctx, pool
}

func pendingTestBet(t *testing.T, w wallet.Wallet, external, key, amount string) wager.Bet {
	t.Helper()
	value, err := money.New(amount, w.Balance().Currency())
	if err != nil {
		t.Fatal(err)
	}
	b, err := wager.NewBet(rand.Text(), wager.BetRequest{ProviderID: "provider-a", ExternalTransactionID: external, IdempotencyKey: key, WalletID: w.ID(), PlayerID: w.PlayerID(), RoundID: "round", GameID: "game", Money: value}, time.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBetStoreDurableReplayAndRejectionIntegration(t *testing.T) {
	ctx, pool := betTestDatabase(t)
	w := repositoryTestWallet(t, "bet-wallet", "bet-player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	store := NewBetStore(pool)
	first := pendingTestBet(t, w, "external-1", "caller-key", "25.00")
	result, replay, err := store.ProcessBet(ctx, first)
	if err != nil || replay || result.Status() != wager.Processed || result.Balance().MinorUnits() != 7500 || result.WalletVersion() != 2 {
		t.Fatalf("Aposta não processada: %v", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 2, 2, 4})
	second := pendingTestBet(t, w, "external-2", "caller-key-2", "25.00")
	if _, _, err := store.ProcessBet(ctx, second); err != nil {
		t.Fatal(err)
	}
	// Um novo store (sem memória de deduplicação) consulta o snapshot original.
	repeated, wasReplay, err := NewBetStore(pool).ProcessBet(ctx, first)
	if err != nil || !wasReplay || repeated != result {
		t.Fatalf("Replay perdeu resultado original: %v", err)
	}
	rejected, _, err := store.ProcessBet(ctx, pendingTestBet(t, w, "external-3", "caller-key-3", "80.00"))
	if err != nil || rejected.Status() != wager.Rejected || rejected.FailureCode() != wager.InsufficientFunds || rejected.Balance().MinorUnits() != 5000 || rejected.WalletVersion() != 3 {
		t.Fatalf("Rejeição não auditada: %v", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 4, 3, 7})
	r := first.Request()
	r.Money, _ = money.New("30.00", "BRL")
	changed, _ := wager.NewBet(rand.Text(), r, first.CreatedAt())
	if _, _, err := store.ProcessBet(ctx, changed); !errors.Is(err, wager.ErrIdempotencyConflict) {
		t.Fatalf("Chave com outro payload: %v", err)
	}
	r = first.Request()
	r.IdempotencyKey = "another-key"
	changed, _ = wager.NewBet(rand.Text(), r, first.CreatedAt())
	if _, _, err := store.ProcessBet(ctx, changed); !errors.Is(err, wager.ErrExternalConflict) {
		t.Fatalf("ID externo com outra chave: %v", err)
	}
	// Chaves têm escopo de provedor. Outro provedor pode usar os mesmos identificadores.
	r = first.Request()
	r.ProviderID = "provider-b"
	changed, _ = wager.NewBet(rand.Text(), r, first.CreatedAt())
	if _, replay, err := store.ProcessBet(ctx, changed); err != nil || replay {
		t.Fatalf("Provedores independentes colidiram: %v", err)
	}
	loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
	if err != nil || loaded.Balance().MinorUnits() != 2500 || loaded.Version() != 4 {
		t.Fatal("Estado final incorreto", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 5, 4, 9})
	var stringsOnly bool
	if err := pool.QueryRow(ctx, `SELECT bool_and(jsonb_typeof(payload->'data'->'money'->'amount')='string') FROM outbox_events`).Scan(&stringsOnly); err != nil || !stringsOnly {
		t.Fatal("Evento perdeu precisão monetária", err)
	}
}

func TestBetStoreRollsBackAllFinancialChangesIntegration(t *testing.T) {
	for _, injection := range []string{
		`ALTER TABLE wallet_ledger_entries ADD CONSTRAINT injected_bet_failure CHECK (direction <> 'DEBIT')`,
		`ALTER TABLE outbox_events ADD CONSTRAINT injected_bet_failure CHECK (event_type <> 'WalletBalanceChanged' OR transaction_id LIKE 'opening:%')`,
		`CREATE FUNCTION reject_bet_commit() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected commit failure' USING ERRCODE='23514'; END; $$;
   CREATE CONSTRAINT TRIGGER injected_bet_failure AFTER INSERT ON wager_transactions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.origin='EXTERNAL') EXECUTE FUNCTION reject_bet_commit()`,
	} {
		t.Run(injection[:20], func(t *testing.T) {
			ctx, pool := betTestDatabase(t)
			w := repositoryTestWallet(t, "rollback", "player", "100.00", "BRL")
			if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, injection); err != nil {
				t.Fatal(err)
			}
			_, replay, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "external", "key", "25.00"))
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || replay {
				t.Fatalf("Falha injetada perdida: %v", err)
			}
			loaded, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
			if err != nil || loaded != w {
				t.Fatal("Saldo parcial confirmado", err)
			}
			assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
		})
	}
}

func TestBetStoreCancellationAndIndependentWalletsIntegration(t *testing.T) {
	ctx, pool := betTestDatabase(t)
	first := repositoryTestWallet(t, "locked", "first-player", "100.00", "BRL")
	other := repositoryTestWallet(t, "independent", "other-player", "100.00", "BRL")
	for _, w := range []wallet.Wallet{first, other} {
		if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, first.ID()); err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	pending := pendingTestBet(t, first, "blocked", "blocked-key", "1.00")
	done := make(chan error, 1)
	go func() { _, _, err := NewBetStore(pool).ProcessBet(blockedCtx, pending); done <- err }()
	independentCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if _, _, err := NewBetStore(pool).ProcessBet(independentCtx, pendingTestBet(t, other, "independent", "independent-key", "1.00")); err != nil {
		t.Fatalf("Lock de outra carteira bloqueou progresso: %v", err)
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Espera não respeitou prazo", err)
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertOpeningCounts(t, ctx, pool, first.ID(), [4]int{1, 1, 1, 2})
	if _, _, err := NewBetStore(pool).ProcessBet(ctx, pending); err != nil {
		t.Fatal("Retry após cancelamento falhou", err)
	}
}

func TestBetSchemaGuardsAndMigrationCycleIntegration(t *testing.T) {
	ctx, pool := betTestDatabase(t)
	runOpeningMigration(t, ctx, pool, "000003_bet.down.sql")
	runOpeningMigration(t, ctx, pool, "000003_bet.up.sql")
	w := repositoryTestWallet(t, "protected-bet", "player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	completed, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "external", "key", "80.00"))
	if err != nil {
		t.Fatal(err)
	}
	rejected, _, err := NewBetStore(pool).ProcessBet(ctx, pendingTestBet(t, w, "rejected", "rejected-key", "80.00"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE wallets SET balance_cents=balance_cents-1,version=version+1`,
		`UPDATE wallets SET version=version+1`,
		`UPDATE wallets SET player_id='someone-else'`,
		`UPDATE wager_transactions SET status='PENDING' WHERE origin='EXTERNAL'`,
		`DELETE FROM wager_transactions WHERE origin='EXTERNAL'`,
		`UPDATE outbox_events SET payload='{}'::jsonb`,
		`DELETE FROM wallet_ledger_entries WHERE direction='DEBIT'`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatalf("Banco aceitou alteração proibida: %s", statement)
		}
	}
	// Nem uma inserção posterior pode criar ledger para uma rejeição já confirmada.
	_, err = pool.Exec(ctx, `INSERT INTO wallet_ledger_entries (id,wallet_id,transaction_id,direction,currency,amount_cents,balance_before_cents,balance_after_cents,created_at)
  VALUES ('forbidden',$1,$2,'CREDIT','BRL',8000,2000,10000,$3)`, w.ID(), rejected.ID(), rejected.UpdatedAt())
	if err == nil {
		t.Fatal("Banco aceitou ledger para rejeição")
	}
	// Uma transação terminal sem ledger/outbox não pode ser confirmada.
	_, err = pool.Exec(ctx, `INSERT INTO wager_transactions SELECT 'incomplete',origin,kind,wallet_id,player_id,currency,amount_cents,status,
  balance_after_cents,wallet_version,created_at,updated_at,provider_id,'other-external','other-key',payload_hash,round_id,game_id,balance_before_cents,failure_code
  FROM wager_transactions WHERE id=$1`, rejected.ID())
	if err == nil {
		t.Fatal("Banco aceitou rejeição sem evento")
	}
	data, err := pool.Query(ctx, `SELECT id FROM wager_transactions WHERE id=$1`, completed.ID())
	if err != nil {
		t.Fatal(err)
	}
	data.Close()
}

func TestBetKeyConflictAcrossWalletsIntegration(t *testing.T) {
	ctx, pool := betTestDatabase(t)
	first := repositoryTestWallet(t, "key-first", "key-player-first", "100.00", "BRL")
	second := repositoryTestWallet(t, "key-second", "key-player-second", "100.00", "BRL")
	for _, w := range []wallet.Wallet{first, second} {
		if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i, w := range []wallet.Wallet{first, second} {
		pending := pendingTestBet(t, w, []string{"external-first", "external-second"}[i], "shared-key", "10.00")
		workers.Go(func() { <-start; _, _, err := NewBetStore(pool).ProcessBet(ctx, pending); results <- err })
	}
	close(start)
	workers.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, wager.ErrIdempotencyConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("Chave compartilhada em carteiras diferentes duplicou operação")
	}
	var total int64
	if err := pool.QueryRow(ctx, `SELECT sum(balance_cents)::BIGINT FROM wallets`).Scan(&total); err != nil || total != 19000 {
		t.Fatal("Conflito alterou as duas carteiras", err)
	}
}
