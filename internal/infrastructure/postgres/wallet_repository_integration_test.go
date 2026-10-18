//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func walletRepositoryTestTransaction(t *testing.T) (context.Context, *pgxpool.Pool, pgx.Tx) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("Defina TEST_DATABASE_URL e aplique as migrations antes dos testes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal("Configuração inválida de TEST_DATABASE_URL")
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Erro ao iniciar transação no PostgreSQL: %v", err)
	}
	t.Cleanup(func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rollbackCancel()
		if err := tx.Rollback(rollbackCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("Erro ao reverter dados de teste: %v", err)
		}
	})
	return ctx, pool, tx
}

func repositoryTestWallet(t *testing.T, id, playerID, amount, currency string) wallet.Wallet {
	t.Helper()
	balance, err := money.New(amount, currency)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.October, 18, 13, 30, 0, 123000000, time.UTC)
	w, err := wallet.NewWallet(id, playerID, balance, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWalletRepositoryCreateIntegration(t *testing.T) {
	cases := []struct {
		name     string
		amount   string
		currency string
	}{
		{name: "saldo positivo", amount: "100.05", currency: "BRL"},
		{name: "saldo zero", amount: "0.00", currency: "BRL"},
		{name: "máximo em outra moeda", amount: "92233720368547758.07", currency: "USD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, tx := walletRepositoryTestTransaction(t)
			repository := NewWalletRepository(tx)
			id := fmt.Sprintf("test-wallet-%d'; --", time.Now().UnixNano())
			w := repositoryTestWallet(t, id, id+"-player", tc.amount, tc.currency)
			if err := repository.Create(ctx, w); err != nil {
				t.Fatal(err)
			}

			var playerID, currency string
			var balanceCents, version int64
			var createdAt, updatedAt time.Time
			err := tx.QueryRow(ctx, `
				SELECT player_id, balance_cents, currency, version, created_at, updated_at
				FROM wallets WHERE id = $1
			`, id).Scan(&playerID, &balanceCents, &currency, &version, &createdAt, &updatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if playerID != w.PlayerID() || balanceCents != w.Balance().MinorUnits() || currency != w.Balance().Currency() || version != w.Version() {
				t.Fatal("INSERT alterou identidade, saldo, moeda ou versão")
			}
			if !createdAt.Equal(w.CreatedAt()) || !updatedAt.Equal(w.UpdatedAt()) {
				t.Fatal("INSERT alterou os instantes da carteira")
			}
		})
	}
}

func TestWalletRepositoryCreateRejectsDuplicatesIntegration(t *testing.T) {
	for _, duplicate := range []string{"id", "jogador e moeda"} {
		t.Run(duplicate, func(t *testing.T) {
			ctx, _, tx := walletRepositoryTestTransaction(t)
			repository := NewWalletRepository(tx)
			prefix := fmt.Sprintf("test-duplicate-%d", time.Now().UnixNano())
			first := repositoryTestWallet(t, prefix, prefix+"-player", "100.00", "BRL")
			if err := repository.Create(ctx, first); err != nil {
				t.Fatal(err)
			}
			id, playerID := prefix+"-second", first.PlayerID()
			if duplicate == "id" {
				id, playerID = first.ID(), prefix+"-other-player"
			}
			second := repositoryTestWallet(t, id, playerID, "50.00", "BRL")
			if err := repository.Create(ctx, second); !errors.Is(err, wallet.ErrAlreadyExists) {
				t.Fatalf("Esperava ErrAlreadyExists; recebeu %v", err)
			}
		})
	}
}

func TestWalletRepositoryCreateAllowsDifferentCurrenciesIntegration(t *testing.T) {
	ctx, _, tx := walletRepositoryTestTransaction(t)
	repository := NewWalletRepository(tx)
	prefix := fmt.Sprintf("test-currencies-%d", time.Now().UnixNano())
	for _, currency := range []string{"BRL", "USD"} {
		w := repositoryTestWallet(t, prefix+currency, prefix+"-player", "0.00", currency)
		if err := repository.Create(ctx, w); err != nil {
			t.Fatalf("Mesmo jogador deve poder ter moedas diferentes: %v", err)
		}
	}
}

func TestWalletRepositoryCreateRespectsRollbackIntegration(t *testing.T) {
	ctx, pool, tx := walletRepositoryTestTransaction(t)
	id := fmt.Sprintf("test-rollback-%d", time.Now().UnixNano())
	w := repositoryTestWallet(t, id, id+"-player", "100.00", "BRL")
	if err := NewWalletRepository(tx).Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM wallets WHERE id = $1", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("Repositório confirmou dados fora da transação recebida")
	}
}

func TestWalletRepositoryCreateRejectsInvalidWalletAndCanceledContextIntegration(t *testing.T) {
	ctx, _, tx := walletRepositoryTestTransaction(t)
	repository := NewWalletRepository(tx)
	if err := repository.Create(ctx, wallet.Wallet{}); !errors.Is(err, wallet.ErrInvalidID) {
		t.Fatalf("Carteira inválida deve ser rejeitada antes do INSERT: %v", err)
	}
	id := fmt.Sprintf("test-canceled-%d", time.Now().UnixNano())
	w := repositoryTestWallet(t, id, id+"-player", "100.00", "BRL")
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := repository.Create(canceledCtx, w); !errors.Is(err, context.Canceled) {
		t.Fatalf("Esperava context.Canceled; recebeu %v", err)
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM wallets WHERE id = $1", id).Scan(&count); err != nil {
		t.Fatalf("Validações não devem executar SQL inválido ou abortar a transação: %v", err)
	}
	if count != 0 {
		t.Fatal("Contexto cancelado não deve inserir carteira")
	}
}

func TestWalletRepositoryFindByIDIntegration(t *testing.T) {
	cases := []struct {
		name     string
		amount   string
		currency string
		version  int64
	}{
		{name: "saldo zero", amount: "0.00", currency: "BRL", version: 1},
		{name: "carteira movimentada", amount: "75.05", currency: "BRL", version: 8},
		{name: "limites numéricos", amount: "92233720368547758.07", currency: "USD", version: math.MaxInt64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, tx := walletRepositoryTestTransaction(t)
			repository := NewWalletRepository(tx)
			id := fmt.Sprintf("test-find-%d'; --", time.Now().UnixNano())
			initial := repositoryTestWallet(t, id, id+"-player", tc.amount, tc.currency)
			stored, err := wallet.Rehydrate(initial.ID(), initial.PlayerID(), initial.Balance(), tc.version,
				initial.CreatedAt(), initial.CreatedAt().Add(24*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.Create(ctx, stored); err != nil {
				t.Fatal(err)
			}

			got, err := repository.FindByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID() != stored.ID() || got.PlayerID() != stored.PlayerID() || got.Balance() != stored.Balance() || got.Version() != stored.Version() {
				t.Fatal("Busca alterou identidades, saldo, moeda ou versão persistidos")
			}
			if !got.CreatedAt().Equal(stored.CreatedAt()) || !got.UpdatedAt().Equal(stored.UpdatedAt()) {
				t.Fatal("Busca alterou os instantes persistidos")
			}
			if got.CreatedAt().Location() != time.UTC || got.UpdatedAt().Location() != time.UTC {
				t.Fatal("Carteira carregada deve representar os instantes em UTC")
			}
			again, err := repository.FindByID(ctx, id)
			if err != nil || again != got {
				t.Fatalf("Repetir a busca não deve modificar o estado: %v", err)
			}
		})
	}
}

func TestWalletRepositoryFindByIDNotFoundIntegration(t *testing.T) {
	ctx, _, tx := walletRepositoryTestTransaction(t)
	id := fmt.Sprintf("test-missing-%d", time.Now().UnixNano())
	got, err := NewWalletRepository(tx).FindByID(ctx, id)
	if !errors.Is(err, wallet.ErrNotFound) || got != (wallet.Wallet{}) {
		t.Fatalf("Esperava ErrNotFound e carteira vazia; recebeu %v", err)
	}
}

func TestWalletRepositoryFindByIDRejectsInvalidInputIntegration(t *testing.T) {
	ctx, _, tx := walletRepositoryTestTransaction(t)
	repository := NewWalletRepository(tx)
	id := fmt.Sprintf("test-find-context-%d", time.Now().UnixNano())
	w := repositoryTestWallet(t, id, id+"-player", "25.00", "BRL")
	if err := repository.Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	for _, invalidID := range []string{"", " \t\n"} {
		got, err := repository.FindByID(ctx, invalidID)
		if !errors.Is(err, wallet.ErrInvalidID) || got != (wallet.Wallet{}) {
			t.Fatalf("Esperava ErrInvalidID e carteira vazia; recebeu %v", err)
		}
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	got, err := repository.FindByID(canceledCtx, id)
	if !errors.Is(err, context.Canceled) || got != (wallet.Wallet{}) {
		t.Fatalf("Esperava context.Canceled e carteira vazia; recebeu %v", err)
	}
	expiredCtx, expireCancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer expireCancel()
	got, err = repository.FindByID(expiredCtx, id)
	if !errors.Is(err, context.DeadlineExceeded) || got != (wallet.Wallet{}) {
		t.Fatalf("Esperava context.DeadlineExceeded e carteira vazia; recebeu %v", err)
	}
	if _, err := repository.FindByID(ctx, id); err != nil {
		t.Fatalf("Entradas rejeitadas não devem abortar a transação: %v", err)
	}
}

func TestWalletRepositoryFindByIDPreservesDatabaseErrorIntegration(t *testing.T) {
	ctx, _, tx := walletRepositoryTestTransaction(t)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := NewWalletRepository(tx).FindByID(ctx, "wallet-1")
	if !errors.Is(err, pgx.ErrTxClosed) || errors.Is(err, wallet.ErrNotFound) || got != (wallet.Wallet{}) {
		t.Fatalf("Falha de infraestrutura deve preservar a causa: %v", err)
	}
}

func TestWalletRepositoryFindByIDRejectsInvalidStoredStateIntegration(t *testing.T) {
	ctx, _, tx := walletRepositoryTestTransaction(t)
	repository := NewWalletRepository(tx)
	id := fmt.Sprintf("test-invalid-state-%d", time.Now().UnixNano())
	w := repositoryTestWallet(t, id, id+"-player", "25.00", "BRL")
	if err := repository.Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	// Simula um estado persistido que viola uma invariante temporal do domínio.
	if _, err := tx.Exec(ctx, "UPDATE wallets SET updated_at = created_at - INTERVAL '1 second' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	got, err := repository.FindByID(ctx, id)
	if !errors.Is(err, wallet.ErrInvalidTime) || got != (wallet.Wallet{}) {
		t.Fatalf("Reidratação deve rejeitar o estado inválido: %v", err)
	}
}

func TestWalletRepositoryFindByIDDoesNotReadUncommittedDataIntegration(t *testing.T) {
	ctx, pool, tx := walletRepositoryTestTransaction(t)
	id := fmt.Sprintf("test-isolation-%d", time.Now().UnixNano())
	w := repositoryTestWallet(t, id, id+"-player", "0.00", "BRL")
	if err := NewWalletRepository(tx).Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := NewWalletRepository(pool).FindByID(ctx, id)
	if !errors.Is(err, wallet.ErrNotFound) || got != (wallet.Wallet{}) {
		t.Fatalf("Outra conexão não deve ler a carteira antes do commit: %v", err)
	}
}
