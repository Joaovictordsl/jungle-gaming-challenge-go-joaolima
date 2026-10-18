package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

// DBTX permite executar o repositório com o pool ou com uma transação SQL.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Estas verificações garantem em compilação que os dois tipos atendem ao contrato.
var (
	_ DBTX              = (*pgxpool.Pool)(nil)
	_ DBTX              = (pgx.Tx)(nil)
	_ wallet.Repository = (*WalletRepository)(nil)
)

type WalletRepository struct {
	db DBTX
}

func NewWalletRepository(db DBTX) *WalletRepository {
	return &WalletRepository{db: db}
}

// Create insere a carteira usando o executor recebido, sem abrir ou confirmar
// transações. O caso de uso coordenará carteira, ledger e eventos no mesmo commit.
func (r *WalletRepository) Create(ctx context.Context, w wallet.Wallet) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.Validate(); err != nil {
		return fmt.Errorf("criar carteira: %w", err)
	}

	const query = `
		INSERT INTO wallets (
			id, player_id, balance_cents, currency, version, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := r.db.Exec(ctx, query,
		w.ID(),
		w.PlayerID(),
		w.Balance().MinorUnits(),
		w.Balance().Currency(),
		w.Version(),
		w.CreatedAt(),
		w.UpdatedAt(),
	)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23505" {
			return fmt.Errorf("criar carteira: %w", wallet.ErrAlreadyExists)
		}
		return walletPersistenceError("inserir carteira", err)
	}
	return nil
}

// FindByID lê o estado persistido sem movimentar saldo ou alterar a versão.
func (r *WalletRepository) FindByID(ctx context.Context, id string) (wallet.Wallet, error) {
	if err := ctx.Err(); err != nil {
		return wallet.Wallet{}, err
	}
	if strings.TrimSpace(id) == "" {
		return wallet.Wallet{}, wallet.ErrInvalidID
	}

	const query = `
		SELECT id, player_id, balance_cents, currency, version, created_at, updated_at
		FROM wallets
		WHERE id = $1
	`
	var storedID, playerID, currency string
	var balanceCents, version int64
	var createdAt, updatedAt time.Time
	err := r.db.QueryRow(ctx, query, id).Scan(
		&storedID, &playerID, &balanceCents, &currency, &version, &createdAt, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.Wallet{}, fmt.Errorf("buscar carteira: %w", wallet.ErrNotFound)
	}
	if err != nil {
		return wallet.Wallet{}, walletPersistenceError("buscar carteira", err)
	}

	balance, err := money.FromMinorUnits(balanceCents, currency)
	if err != nil {
		return wallet.Wallet{}, fmt.Errorf("reconstruir saldo persistido: %w", err)
	}
	w, err := wallet.Rehydrate(storedID, playerID, balance, version, createdAt, updatedAt)
	if err != nil {
		return wallet.Wallet{}, fmt.Errorf("reidratar carteira: %w", err)
	}
	return w, nil
}
