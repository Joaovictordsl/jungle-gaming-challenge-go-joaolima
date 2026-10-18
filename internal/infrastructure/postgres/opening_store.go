package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"jungle-gaming-challenge-go-joaolima/internal/application/event"
	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

var _ applicationwallet.OpeningStore = (*OpeningStore)(nil)

type OpeningStore struct{ pool *pgxpool.Pool }

func NewOpeningStore(pool *pgxpool.Pool) *OpeningStore { return &OpeningStore{pool: pool} }

// SaveOpening confirma todos os registros em uma transação e não publica eventos.
func (s *OpeningStore) SaveOpening(ctx context.Context, w wallet.Wallet) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.ValidateInitialState(); err != nil {
		return fmt.Errorf("validar abertura: %w", err)
	}

	var opening wager.Opening
	var entry ledger.Entry
	var snapshots []event.Snapshot
	if w.Balance().MinorUnits() > 0 {
		pending, err := wager.NewOpening(w)
		if err != nil {
			return err
		}
		opening, err = pending.Process()
		if err != nil {
			return err
		}
		zero, err := money.Zero(w.Balance().Currency())
		if err != nil {
			return err
		}
		entry, err = ledger.New("ledger:"+opening.ID(), w.ID(), opening.ID(), ledger.Credit,
			w.Balance(), zero, w.Balance(), w.CreatedAt())
		if err != nil {
			return err
		}
		processed, err := event.NewWagerTransactionProcessed(opening)
		if err != nil {
			return err
		}
		changed, err := event.NewWalletBalanceChanged(opening, entry)
		if err != nil {
			return err
		}
		snapshots = []event.Snapshot{processed, changed}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return openingStoreError(ctx, "iniciar transação de abertura", err)
	}
	defer func() {
		// O contexto da chamada pode estar cancelado. A limpeza usa prazo próprio.
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	if err := NewWalletRepository(tx).Create(ctx, w); err != nil {
		return openingStoreError(ctx, "persistir carteira", err)
	}
	if w.Balance().MinorUnits() > 0 {
		_, err := tx.Exec(ctx, `INSERT INTO wager_transactions (
			id, origin, kind, wallet_id, player_id, currency, amount_cents, status,
			balance_after_cents, wallet_version, created_at, updated_at
		) VALUES ($1, 'INTERNAL', 'OPENING', $2, $3, $4, $5, $6, $5, 1, $7, $7)`,
			opening.ID(), w.ID(), w.PlayerID(), w.Balance().Currency(), w.Balance().MinorUnits(), opening.Status(), w.CreatedAt())
		if err != nil {
			return openingStoreError(ctx, "persistir OPENING", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, currency, amount_cents,
			balance_before_cents, balance_after_cents, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			entry.ID(), entry.WalletID(), entry.TransactionID(), entry.Direction(), entry.Amount().Currency(),
			entry.Amount().MinorUnits(), entry.BalanceBefore().MinorUnits(), entry.BalanceAfter().MinorUnits(), entry.CreatedAt())
		if err != nil {
			return openingStoreError(ctx, "persistir crédito inicial", err)
		}
		for _, snapshot := range snapshots {
			payload, err := json.Marshal(snapshot)
			if err != nil {
				return fmt.Errorf("serializar evento de abertura: %w", err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO outbox_events (
				id, transaction_id, aggregate_id, event_type, payload, occurred_at, next_attempt_at
			) VALUES ($1, $2, $3, $4, $5, $6, $6)`,
				snapshot.EventID(), opening.ID(), snapshot.AggregateID(), snapshot.EventType(), payload, snapshot.OccurredAt())
			if err != nil {
				return openingStoreError(ctx, "persistir evento de abertura", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return openingStoreError(ctx, "confirmar abertura", err)
	}
	return nil
}

func openingStoreError(ctx context.Context, operation string, err error) error {
	if cause := ctx.Err(); cause != nil {
		return walletPersistenceError(operation, errors.Join(cause, err))
	}
	return walletPersistenceError(operation, err)
}
