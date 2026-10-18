//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

// Cada teste pode confirmar transações reais sem tocar nas tabelas da aplicação.
func openingStoreTestDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("Defina TEST_DATABASE_URL para executar os testes de integração")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal("Configuração inválida de TEST_DATABASE_URL")
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{"opening_test_" + strings.ToLower(rand.Text())}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("Erro ao remover schema isolado do teste: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("Configuração inválida de TEST_DATABASE_URL")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, file := range []string{"000001_create_wallets.up.sql", "000002_wallet_opening.up.sql"} {
		runOpeningMigration(t, ctx, pool, file)
	}
	return ctx, pool
}

func runOpeningMigration(t *testing.T, ctx context.Context, pool *pgxpool.Pool, file string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(data)); err != nil {
		t.Fatalf("Migration %s falhou: %v", file, err)
	}
}

func assertOpeningCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, walletID string, want [4]int) {
	t.Helper()
	var got [4]int
	err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM wallets WHERE id = $1),
		(SELECT count(*) FROM wager_transactions WHERE wallet_id = $1),
		(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1),
		(SELECT count(*) FROM outbox_events WHERE aggregate_id = $1)`, walletID).Scan(&got[0], &got[1], &got[2], &got[3])
	if err != nil || got != want {
		t.Fatalf("Contagens [carteira transação ledger outbox]: recebeu %v, esperava %v, erro %v", got, want, err)
	}
}

func TestOpeningStorePersistsCompleteOpeningIntegration(t *testing.T) {
	ctx, pool := openingStoreTestDatabase(t)
	store := NewOpeningStore(pool)
	for _, tc := range []struct{ name, amount, currency string }{
		{"zero", "0.00", "BRL"}, {"positivo", "100.05", "BRL"}, {"máximo", "92233720368547758.07", "USD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := repositoryTestWallet(t, tc.name, "player-"+tc.name, tc.amount, tc.currency)
			if err := store.SaveOpening(ctx, w); err != nil {
				t.Fatal(err)
			}
			got, err := NewWalletRepository(pool).FindByID(ctx, w.ID())
			if err != nil || got != w {
				t.Fatalf("Carteira confirmada foi alterada: %v", err)
			}
			if w.Balance().MinorUnits() == 0 {
				assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 0, 0, 0})
				return
			}
			assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
			var origin, kind, status, player, currency, direction string
			var amount, before, after, version int64
			err = pool.QueryRow(ctx, `SELECT t.origin, t.kind, t.status, t.player_id, t.currency,
				t.amount_cents, l.direction, l.balance_before_cents, l.balance_after_cents, t.wallet_version
				FROM wager_transactions t JOIN wallet_ledger_entries l ON l.transaction_id = t.id
				WHERE t.wallet_id = $1`, w.ID()).Scan(&origin, &kind, &status, &player, &currency, &amount, &direction, &before, &after, &version)
			if err != nil || origin != "INTERNAL" || kind != "OPENING" || status != "PROCESSED" || player != w.PlayerID() ||
				currency != tc.currency || amount != w.Balance().MinorUnits() || direction != "CREDIT" || before != 0 || after != amount || version != 1 {
				t.Fatalf("Transação e ledger devem representar exatamente a abertura: %v", err)
			}
			rows, err := pool.Query(ctx, `SELECT event_type, payload, attempts, published_at FROM outbox_events WHERE aggregate_id = $1`, w.ID())
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var eventType string
				var payload []byte
				var attempts int
				var publishedAt *time.Time
				if err := rows.Scan(&eventType, &payload, &attempts, &publishedAt); err != nil {
					t.Fatal(err)
				}
				var snapshot struct {
					EventID, EventType, AggregateID, CorrelationID string
					OccurredAt                                     time.Time
					Version                                        int
					Data                                           struct {
						WalletID, TransactionID, Direction string
						WalletVersion                      int64
						Money, BalanceBefore, BalanceAfter struct{ Amount, Currency string }
					}
				}
				if err := json.Unmarshal(payload, &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.EventType != eventType || snapshot.AggregateID != w.ID() || snapshot.EventID == "" || snapshot.CorrelationID == "" ||
					snapshot.Version != 1 || !snapshot.OccurredAt.Equal(w.CreatedAt()) || snapshot.Data.WalletVersion != 1 ||
					snapshot.Data.Money.Amount != tc.amount || snapshot.Data.Money.Currency != tc.currency || publishedAt != nil || attempts != 0 {
					t.Fatalf("Snapshot financeiro ou estado inicial de publicação incorreto: %s", payload)
				}
				if eventType == "WalletBalanceChanged" && (snapshot.Data.BalanceBefore.Amount != "0.00" || snapshot.Data.BalanceAfter.Amount != tc.amount || snapshot.Data.Direction != "CREDIT") {
					t.Fatalf("Evento deve representar o crédito exato: %s", payload)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpeningStoreRollsBackEveryStageIntegration(t *testing.T) {
	for _, tc := range []struct{ name, injection string }{
		{"transação", `ALTER TABLE wager_transactions ADD CONSTRAINT injected_failure CHECK (false)`},
		{"ledger", `ALTER TABLE wallet_ledger_entries ADD CONSTRAINT injected_failure CHECK (false)`},
		{"segundo evento", `ALTER TABLE outbox_events ADD CONSTRAINT injected_failure CHECK (event_type <> 'WalletBalanceChanged')`},
		{"estado inicial incoerente", `CREATE FUNCTION corrupt_initial_balance() RETURNS TRIGGER LANGUAGE plpgsql AS $$
			BEGIN UPDATE wallets SET balance_cents = balance_cents - 1 WHERE id = NEW.aggregate_id; RETURN NEW; END; $$;
			CREATE TRIGGER injected_balance_change AFTER INSERT ON outbox_events
			FOR EACH ROW EXECUTE FUNCTION corrupt_initial_balance()`},
		{"commit", `CREATE FUNCTION reject_opening_commit() RETURNS TRIGGER LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'injected commit failure' USING ERRCODE = '23514'; END; $$;
			CREATE CONSTRAINT TRIGGER injected_commit_failure AFTER INSERT ON wallets
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_opening_commit()`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, pool := openingStoreTestDatabase(t)
			if _, err := pool.Exec(ctx, tc.injection); err != nil {
				t.Fatal(err)
			}
			w := repositoryTestWallet(t, "failed-wallet", "failed-player", "100.00", "BRL")
			err := NewOpeningStore(pool).SaveOpening(ctx, w)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("Esperava falha injetada preservada: %v", err)
			}
			assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{})
		})
	}
}

func TestOpeningStoreConcurrentConflictIntegration(t *testing.T) {
	ctx, pool := openingStoreTestDatabase(t)
	store := NewOpeningStore(pool)
	for _, sameID := range []bool{false, true} {
		firstID, secondID, playerID := "first", "second", "same-player"
		if sameID {
			firstID, secondID, playerID = "same-id", "same-id", "other-player"
		}
		first := repositoryTestWallet(t, firstID, playerID, "100.00", "BRL")
		second := repositoryTestWallet(t, secondID, playerID, "50.00", "BRL")
		start := make(chan struct{})
		results := make(chan error, 2)
		var workers sync.WaitGroup
		for _, w := range []wallet.Wallet{first, second} {
			workers.Go(func() {
				<-start
				results <- store.SaveOpening(ctx, w)
			})
		}
		close(start)
		workers.Wait()
		close(results)
		successes, conflicts := 0, 0
		for err := range results {
			if err == nil {
				successes++
			} else if errors.Is(err, wallet.ErrAlreadyExists) {
				conflicts++
			} else {
				t.Fatalf("Erro inesperado: %v", err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("Esperava uma confirmação e um conflito; recebeu %d e %d", successes, conflicts)
		}
		var winnerID string
		if err := pool.QueryRow(ctx, `SELECT id FROM wallets WHERE player_id = $1`, playerID).Scan(&winnerID); err != nil {
			t.Fatal(err)
		}
		assertOpeningCounts(t, ctx, pool, winnerID, [4]int{1, 1, 1, 2})
		if err := store.SaveOpening(ctx, first); !errors.Is(err, wallet.ErrAlreadyExists) {
			t.Fatalf("Repetição deve retornar conflito sem duplicar crédito: %v", err)
		}
		assertOpeningCounts(t, ctx, pool, winnerID, [4]int{1, 1, 1, 2})
	}
}

func TestOpeningStoreRejectsInvalidStateAndCancellationIntegration(t *testing.T) {
	ctx, pool := openingStoreTestDatabase(t)
	store := NewOpeningStore(pool)
	w := repositoryTestWallet(t, "invalid-wallet", "invalid-player", "10.00", "BRL")
	moved, _ := wallet.Rehydrate(w.ID(), w.PlayerID(), w.Balance(), 2, w.CreatedAt(), w.UpdatedAt())
	if err := store.SaveOpening(ctx, moved); !errors.Is(err, wallet.ErrNotInitialState) {
		t.Fatalf("Carteira movimentada não deve abrir novamente: %v", err)
	}
	if err := store.SaveOpening(ctx, wallet.Wallet{}); !errors.Is(err, wallet.ErrInvalidID) {
		t.Fatalf("Carteira vazia deve ser rejeitada: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.SaveOpening(canceled, w); !errors.Is(err, context.Canceled) {
		t.Fatalf("Esperava cancelamento: %v", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{})

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, "LOCK TABLE wallet_ledger_entries IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	deadline, deadlineCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer deadlineCancel()
	if err := store.SaveOpening(deadline, w); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Espera no banco deve respeitar timeout: %v", err)
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{})
	if err := store.SaveOpening(ctx, w); err != nil {
		t.Fatalf("Retry após rollback deve conseguir abrir: %v", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
}

func TestOpeningSchemaProtectsFinancialRecordsIntegration(t *testing.T) {
	ctx, pool := openingStoreTestDatabase(t)
	w := repositoryTestWallet(t, "protected-wallet", "protected-player", "100.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE wallet_ledger_entries SET amount_cents = 1`,
		`DELETE FROM wallet_ledger_entries`,
		`TRUNCATE wallet_ledger_entries`,
		`UPDATE wager_transactions SET status = 'PENDING'`,
		`DELETE FROM wager_transactions`,
		`UPDATE outbox_events SET payload = '{}'::jsonb`,
		`DELETE FROM outbox_events`,
		`TRUNCATE outbox_events`,
		`INSERT INTO wallet_ledger_entries SELECT 'duplicate-entry', wallet_id, transaction_id, direction, currency, amount_cents, balance_before_cents, balance_after_cents, created_at FROM wallet_ledger_entries`,
		`INSERT INTO wager_transactions SELECT 'duplicate-opening', origin, kind, wallet_id, player_id, currency, amount_cents, status, balance_after_cents, wallet_version, created_at, updated_at FROM wager_transactions`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatalf("Banco aceitou alteração financeira proibida: %s", statement)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET attempts = 1, next_attempt_at = next_attempt_at + INTERVAL '1 second'`); err != nil {
		t.Fatalf("Metadados de publicação devem poder evoluir sem alterar o snapshot: %v", err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	other := repositoryTestWallet(t, "incomplete-wallet", "incomplete-player", "10.00", "BRL")
	if err := NewWalletRepository(tx).Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO wager_transactions
		(id, origin, kind, wallet_id, player_id, currency, amount_cents, status, balance_after_cents, wallet_version, created_at, updated_at)
		VALUES ('incomplete-opening', 'INTERNAL', 'OPENING', $1, $2, 'BRL', 1000, 'PROCESSED', 1000, 1, $3, $3)`, other.ID(), other.PlayerID(), other.CreatedAt())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("Banco confirmou OPENING sem seu ledger e eventos")
	}
	assertOpeningCounts(t, ctx, pool, other.ID(), [4]int{})
}

func TestOpeningMigrationCycleIntegration(t *testing.T) {
	ctx, pool := openingStoreTestDatabase(t)
	existing := repositoryTestWallet(t, "existing-wallet", "existing-player", "0.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, existing); err != nil {
		t.Fatal(err)
	}
	runOpeningMigration(t, ctx, pool, "000002_wallet_opening.down.sql")
	runOpeningMigration(t, ctx, pool, "000002_wallet_opening.up.sql")
	if _, err := NewWalletRepository(pool).FindByID(ctx, existing.ID()); err != nil {
		t.Fatalf("Reversão da segunda migration não deve remover carteiras: %v", err)
	}
	w := repositoryTestWallet(t, "cycle-wallet", "cycle-player", "10.00", "BRL")
	if err := NewOpeningStore(pool).SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
}

func TestOpeningStoreFxRegistrationIntegration(t *testing.T) {
	ctx, pool := openingStoreTestDatabase(t)
	var store applicationwallet.OpeningStore
	application := fx.New(Module, fx.Replace(pool), fx.Populate(&store), fx.NopLogger)
	if err := application.Err(); err != nil {
		t.Fatal(err)
	}
	w := repositoryTestWallet(t, "fx-wallet", "fx-player", "10.00", "BRL")
	if err := store.SaveOpening(ctx, w); err != nil {
		t.Fatal(err)
	}
	assertOpeningCounts(t, ctx, pool, w.ID(), [4]int{1, 1, 1, 2})
}
