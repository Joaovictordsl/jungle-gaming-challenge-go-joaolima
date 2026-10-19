package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-challenge-go-joaolima/internal/application/query"
	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

var _ query.Store = (*ReadStore)(nil)

type ReadStore struct{ pool *pgxpool.Pool }

func NewReadStore(pool *pgxpool.Pool) *ReadStore { return &ReadStore{pool: pool} }

const transactionProjection = `SELECT id,origin,kind,status,COALESCE(provider_id,''),COALESCE(external_transaction_id,''),wallet_id,player_id,COALESCE(round_id,''),COALESCE(game_id,''),currency,amount_cents,balance_after_cents,wallet_version,COALESCE(failure_code,''),COALESCE(reference_external_transaction_id,''),COALESCE(reference_transaction_id,''),created_at,updated_at FROM wager_transactions `

func (s *ReadStore) FindTransaction(ctx context.Context, id, scope string) (query.Transaction, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return readTransaction(s.pool.QueryRow(ctx, transactionProjection+`WHERE id=$1 AND ($2='' OR provider_id=$2)`, id, scope))
}
func (s *ReadStore) FindExternal(ctx context.Context, provider, external string) (query.Transaction, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return readTransaction(s.pool.QueryRow(ctx, transactionProjection+`WHERE provider_id=$1 AND external_transaction_id=$2`, provider, external))
}
func readTransaction(row pgx.Row) (query.Transaction, error) {
	var t query.Transaction
	var currency string
	var amount, balance int64
	err := row.Scan(&t.TransactionID, &t.Origin, &t.Kind, &t.Status, &t.ProviderID, &t.ExternalTransactionID, &t.WalletID, &t.PlayerID, &t.RoundID, &t.GameID, &currency, &amount, &balance, &t.WalletVersion, &t.FailureCode, &t.ReferenceExternalTransactionID, &t.ReferenceTransactionID, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, query.ErrNotFound
	}
	if err != nil {
		return t, walletPersistenceError("consultar transação", err)
	}
	t.Money, err = money.FromMinorUnits(amount, currency)
	if err != nil {
		return query.Transaction{}, err
	}
	t.Balance, err = money.FromMinorUnits(balance, currency)
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	return t, err
}

func (s *ReadStore) ListLedger(ctx context.Context, id string, cursor query.Cursor, limit int) (page query.LedgerPage, err error) {
	if limit < 1 || limit > 100 {
		return page, query.ErrInvalidQuery
	}
	if cursor != (query.Cursor{}) && cursor.Validate(id) != nil {
		return page, query.ErrInvalidCursor
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, walletPersistenceError("iniciar consulta de extrato", err)
	}
	defer rollbackFinancial(tx)
	current, err := NewWalletRepository(tx).FindByID(ctx, id)
	if err != nil {
		return page, err
	}
	upper := current.Version()
	if cursor.Version != 0 {
		upper = cursor.UpperVersion
		if upper > current.Version() {
			return page, query.ErrInvalidCursor
		}
	}
	page = query.LedgerPage{WalletID: id, SnapshotVersion: upper, Entries: make([]query.LedgerEntry, 0)}
	rows, err := tx.Query(ctx, `SELECT l.id,l.wallet_id,l.transaction_id,t.kind,l.direction,l.currency,l.amount_cents,l.balance_before_cents,l.balance_after_cents,t.wallet_version,l.created_at
 FROM wallet_ledger_entries l JOIN wager_transactions t ON t.id=l.transaction_id AND t.wallet_id=l.wallet_id
 WHERE t.status='PROCESSED' AND t.kind<>'LOSS' AND l.wallet_id=$1 AND (t.wallet_version,l.id)>($2,$3) AND t.wallet_version<=$4
 ORDER BY t.wallet_version,l.id LIMIT $5`, id, cursor.AfterVersion, cursor.AfterID, upper, limit+1)
	if err != nil {
		return page, walletPersistenceError("consultar extrato", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e query.LedgerEntry
		var currency string
		var amount, before, after int64
		if err := rows.Scan(&e.ID, &e.WalletID, &e.TransactionID, &e.Kind, &e.Direction, &currency, &amount, &before, &after, &e.WalletVersion, &e.CreatedAt); err != nil {
			return page, walletPersistenceError("ler extrato", err)
		}
		e.Money, err = money.FromMinorUnits(amount, currency)
		if err != nil {
			return page, err
		}
		e.BalanceBefore, err = money.FromMinorUnits(before, currency)
		if err != nil {
			return page, err
		}
		e.BalanceAfter, err = money.FromMinorUnits(after, currency)
		if err != nil {
			return page, err
		}
		e.CreatedAt = e.CreatedAt.UTC()
		if _, err := ledger.New(e.ID, e.WalletID, e.TransactionID, e.Direction, e.Money, e.BalanceBefore, e.BalanceAfter, e.CreatedAt); err != nil {
			return page, err
		}
		page.Entries = append(page.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return page, walletPersistenceError("ler extrato", err)
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		last := page.Entries[len(page.Entries)-1]
		page.NextCursor, err = query.EncodeCursor(query.Cursor{Version: 1, WalletID: id, UpperVersion: upper, AfterVersion: last.WalletVersion, AfterID: last.ID})
		if err != nil {
			return page, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return query.LedgerPage{}, walletPersistenceError("concluir consulta de extrato", err)
	}
	return page, nil
}

func (s *ReadStore) Reconcile(ctx context.Context, id string) (query.ReconciliationSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var result query.ReconciliationSnapshot
	var currency string
	var balance int64
	// Uma única instrução vê saldo e todos os lançamentos no mesmo snapshot MVCC.
	err := s.pool.QueryRow(ctx, `SELECT w.id,w.currency,w.balance_cents,
 COALESCE(SUM(CASE l.direction WHEN 'CREDIT' THEN l.amount_cents::NUMERIC ELSE -l.amount_cents::NUMERIC END),0)::TEXT,COUNT(l.id)
 FROM wallets w LEFT JOIN wallet_ledger_entries l ON l.wallet_id=w.id WHERE w.id=$1 GROUP BY w.id,w.currency,w.balance_cents`, id).Scan(&result.WalletID, &currency, &balance, &result.TotalMinorUnits, &result.CheckedEntries)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, wallet.ErrNotFound
	}
	if err != nil {
		return result, walletPersistenceError("reconciliar carteira", err)
	}
	result.StoredBalance, err = money.FromMinorUnits(balance, currency)
	return result, err
}
