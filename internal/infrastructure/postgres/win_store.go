package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"jungle-gaming-challenge-go-joaolima/internal/application/event"
	applicationwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

const referenceMaxAttempts = 8
const referenceTTL = 5 * time.Minute

var _ applicationwager.WinStore = (*WinStore)(nil)
var _ applicationwager.RefundStore = (*WinStore)(nil)
var _ applicationwager.RollbackStore = (*WinStore)(nil)
var _ applicationwager.ReferenceProcessor = (*WinStore)(nil)

type WinStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewWinStore(pool *pgxpool.Pool) *WinStore {
	return &WinStore{pool: pool, now: func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }}
}

func rollbackFinancial(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (s *WinStore) ProcessWin(ctx context.Context, pending wager.Win) (wager.Win, bool, error) {
	if pending.Kind() != wager.WinKind {
		return wager.Win{}, false, wager.ErrInvalidState
	}
	return s.processReferenced(ctx, pending)
}

func (s *WinStore) ProcessRefund(ctx context.Context, pending wager.Refund) (wager.Refund, bool, error) {
	if pending.Kind() != wager.RefundKind {
		return wager.Refund{}, false, wager.ErrInvalidState
	}
	return s.processReferenced(ctx, pending)
}

func (s *WinStore) ProcessRollback(ctx context.Context, pending wager.Rollback) (wager.Rollback, bool, error) {
	if pending.Kind() != wager.RollbackKind {
		return wager.Rollback{}, false, wager.ErrInvalidState
	}
	return s.processReferenced(ctx, pending)
}

func (s *WinStore) processReferenced(ctx context.Context, pending wager.ReferencedTransaction) (result wager.ReferencedTransaction, replay bool, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	if err = pending.Validate(); err != nil {
		return
	}
	if pending.Status() != wager.Pending {
		return wager.Win{}, false, wager.ErrInvalidState
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			result = wager.Win{}
			replay = false
			err = openingStoreError(ctx, "processar operação referenciada", err)
		}
	}()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return wager.Win{}, false, err
	}
	defer rollbackFinancial(tx)
	input := pending.Request()
	hash, err := pending.PayloadHash()
	if err != nil {
		return wager.Win{}, false, err
	}
	if existing, found, err := findReferencedReplay(ctx, tx, input, hash); err != nil || found {
		return existing, found, err
	}
	current, err := lockWinWallet(ctx, tx, input.BetRequest)
	if err != nil {
		return wager.Win{}, false, err
	}
	if existing, found, err := findReferencedReplay(ctx, tx, input, hash); err != nil || found {
		return existing, found, err
	}
	reference, err := findWinReference(ctx, tx, input)
	if err != nil {
		return wager.Win{}, false, err
	}
	completed, moved, err := pending.Apply(current, reference, pending.CreatedAt())
	if err != nil {
		return wager.Win{}, false, err
	}
	var deadline, next any
	if completed.Status() == wager.PendingReference {
		deadline = completed.UpdatedAt().Add(referenceTTL)
		next = completed.UpdatedAt().Add(time.Second)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO wager_transactions
  (id,origin,kind,wallet_id,player_id,currency,amount_cents,status,balance_after_cents,wallet_version,
   created_at,updated_at,provider_id,external_transaction_id,idempotency_key,payload_hash,round_id,game_id,balance_before_cents,failure_code,
   reference_external_transaction_id,reference_transaction_id,reference_deadline,next_reference_attempt_at)
  VALUES ($1,'EXTERNAL',$23,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
  ON CONFLICT DO NOTHING`, completed.ID(), input.WalletID, input.PlayerID, input.Money.Currency(), input.Money.MinorUnits(), completed.Status(),
		completed.Balance().MinorUnits(), completed.WalletVersion(), completed.CreatedAt(), completed.UpdatedAt(), input.ProviderID, input.ExternalTransactionID,
		input.IdempotencyKey, hash, input.RoundID, input.GameID, completed.BalanceBefore().MinorUnits(), nullableString(completed.FailureCode()),
		nullableString(input.ReferenceExternalTransactionID), nullableString(completed.ReferenceID()), deadline, next, completed.Kind())
	if err != nil {
		return wager.Win{}, false, err
	}
	if tag.RowsAffected() == 0 {
		existing, found, err := findReferencedReplay(ctx, tx, input, hash)
		if err != nil || found {
			return existing, found, err
		}
		return wager.Win{}, false, fmt.Errorf("colisão inesperada de identidade financeira")
	}
	if err := saveReferencedEffects(ctx, tx, completed, current, moved, true); err != nil {
		return wager.Win{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return wager.Win{}, false, err
	}
	return completed, false, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func lockWinWallet(ctx context.Context, tx pgx.Tx, input wager.BetRequest) (wallet.Wallet, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, input.WalletID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.Wallet{}, wallet.ErrNotFound
	}
	if err != nil {
		return wallet.Wallet{}, err
	}
	return NewWalletRepository(tx).FindByID(ctx, id)
}

func findReferencedReplay(ctx context.Context, tx pgx.Tx, input wager.WinRequest, hash string) (wager.Win, bool, error) {
	var id, key, storedHash string
	err := tx.QueryRow(ctx, `SELECT id,idempotency_key,payload_hash FROM wager_transactions
  WHERE provider_id=$1 AND (idempotency_key=$2 OR external_transaction_id=$3)
  ORDER BY (idempotency_key=$2) DESC LIMIT 1`, input.ProviderID, input.IdempotencyKey, input.ExternalTransactionID).Scan(&id, &key, &storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return wager.Win{}, false, nil
	}
	if err != nil {
		return wager.Win{}, false, err
	}
	if key == input.IdempotencyKey && storedHash != hash {
		return wager.Win{}, false, wager.ErrIdempotencyConflict
	}
	if key != input.IdempotencyKey {
		return wager.Win{}, false, wager.ErrExternalConflict
	}
	win, err := loadReferenced(ctx, tx, id)
	return win, err == nil, err
}

func loadReferenced(ctx context.Context, tx pgx.Tx, id string) (wager.Win, error) {
	var r wager.WinRequest
	var currency, status, failure, referenceID, storedHash, kind, direction string
	var cents, before, after, version int64
	var created, updated time.Time
	err := tx.QueryRow(ctx, `SELECT provider_id,external_transaction_id,idempotency_key,wallet_id,player_id,round_id,game_id,
  currency,amount_cents,status,COALESCE(failure_code,''),COALESCE(reference_external_transaction_id,''),COALESCE(reference_transaction_id,''),
  balance_before_cents,balance_after_cents,wallet_version,created_at,updated_at,payload_hash,kind,
  CASE WHEN kind='ROLLBACK' AND (status='PROCESSED' OR failure_code IN ('ALREADY_REVERSED','BALANCE_OVERFLOW','REVERSAL_INSUFFICIENT_FUNDS'))
    THEN (SELECT CASE r.kind WHEN 'BET' THEN 'CREDIT' ELSE 'DEBIT' END FROM wager_transactions r WHERE r.id=t.reference_transaction_id) ELSE '' END
  FROM wager_transactions t WHERE id=$1 AND kind IN ('WIN','REFUND','ROLLBACK')`, id).Scan(&r.ProviderID, &r.ExternalTransactionID, &r.IdempotencyKey, &r.WalletID, &r.PlayerID, &r.RoundID, &r.GameID,
		&currency, &cents, &status, &failure, &r.ReferenceExternalTransactionID, &referenceID, &before, &after, &version, &created, &updated, &storedHash, &kind, &direction)
	if err != nil {
		return wager.Win{}, err
	}
	r.Money, err = money.FromMinorUnits(cents, currency)
	if err != nil {
		return wager.Win{}, err
	}
	hash, err := r.PayloadHash()
	if kind == wager.RefundKind {
		hash, err = (wager.RefundRequest{WinRequest: r}).PayloadHash()
	}
	if kind == wager.RollbackKind {
		hash, err = (wager.RollbackRequest{WinRequest: r}).PayloadHash()
	}
	if err != nil || hash != storedHash {
		return wager.Win{}, fmt.Errorf("hash persistido incoerente")
	}
	beforeMoney, err := money.FromMinorUnits(before, currency)
	if err != nil {
		return wager.Win{}, err
	}
	afterMoney, err := money.FromMinorUnits(after, currency)
	if err != nil {
		return wager.Win{}, err
	}
	if kind == wager.RefundKind {
		return wager.RehydrateRefund(id, wager.RefundRequest{WinRequest: r}, status, failure, referenceID, beforeMoney, afterMoney, version, created, updated)
	}
	if kind == wager.RollbackKind {
		return wager.RehydrateRollback(id, wager.RollbackRequest{WinRequest: r}, status, failure, referenceID, direction, beforeMoney, afterMoney, version, created, updated)
	}
	return wager.RehydrateWin(id, r, status, failure, referenceID, beforeMoney, afterMoney, version, created, updated)
}

func findWinReference(ctx context.Context, tx pgx.Tx, input wager.WinRequest) (*wager.Reference, error) {
	if input.ReferenceExternalTransactionID == "" {
		return nil, nil
	}
	var reference wager.Reference
	r := &reference.Request
	var currency string
	var cents int64
	err := tx.QueryRow(ctx, `SELECT t.id,t.kind,t.status,t.provider_id,t.external_transaction_id,t.idempotency_key,t.wallet_id,t.player_id,t.round_id,t.game_id,t.currency,t.amount_cents,
  EXISTS (SELECT 1 FROM wager_transactions reversed WHERE reversed.reference_transaction_id=t.id AND reversed.kind IN ('REFUND','ROLLBACK') AND reversed.status='PROCESSED')
  FROM wager_transactions t WHERE t.provider_id=$1 AND t.external_transaction_id=$2`, input.ProviderID, input.ReferenceExternalTransactionID).
		Scan(&reference.ID, &reference.Kind, &reference.Status, &r.ProviderID, &r.ExternalTransactionID, &r.IdempotencyKey, &r.WalletID, &r.PlayerID, &r.RoundID, &r.GameID, &currency, &cents, &reference.Reversed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Money, err = money.FromMinorUnits(cents, currency)
	if err != nil {
		return nil, err
	}
	return &reference, reference.Validate()
}

func saveReferencedEffects(ctx context.Context, tx pgx.Tx, win wager.Win, current, moved wallet.Wallet, initial bool) error {
	var snapshots []event.Snapshot
	r := win.Request()
	switch win.Status() {
	case wager.Processed:
		entry, err := ledger.New("ledger:"+win.ID(), r.WalletID, win.ID(), win.Direction(), r.Money, current.Balance(), moved.Balance(), win.UpdatedAt())
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE wallets SET balance_cents=$2,version=$3,updated_at=$4 WHERE id=$1 AND version=$5`,
			moved.ID(), moved.Balance().MinorUnits(), moved.Version(), moved.UpdatedAt(), current.Version())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("versão inesperada sob lock da carteira")
		}
		_, err = tx.Exec(ctx, `INSERT INTO wallet_ledger_entries(id,wallet_id,transaction_id,direction,currency,amount_cents,balance_before_cents,balance_after_cents,created_at)
   VALUES ($1,$2,$3,$9,$4,$5,$6,$7,$8)`, entry.ID(), r.WalletID, win.ID(), r.Money.Currency(), r.Money.MinorUnits(), current.Balance().MinorUnits(), moved.Balance().MinorUnits(), win.UpdatedAt(), win.Direction())
		if err != nil {
			return err
		}
		processed, err := event.NewReferencedProcessed(win)
		if err != nil {
			return err
		}
		changed, err := event.NewReferencedBalanceChanged(win, entry)
		if err != nil {
			return err
		}
		snapshots = []event.Snapshot{processed, changed}
	case wager.Rejected:
		rejected, err := event.NewReferencedRejected(win)
		if err != nil {
			return err
		}
		snapshots = []event.Snapshot{rejected}
	case wager.PendingReference:
		if initial {
			pending, err := event.NewReferencedPendingReference(win)
			if err != nil {
				return err
			}
			snapshots = []event.Snapshot{pending}
		}
	default:
		return wager.ErrInvalidState
	}
	for _, snapshot := range snapshots {
		data, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO outbox_events(id,transaction_id,aggregate_id,event_type,payload,occurred_at,next_attempt_at)
   VALUES ($1,$2,$3,$4,$5,$6,$6)`, snapshot.EventID(), win.ID(), snapshot.AggregateID(), snapshot.EventType(), data, snapshot.OccurredAt())
		if err != nil {
			return err
		}
	}
	return nil
}

// RetryDueReference usa SKIP LOCKED para dividir pendências entre processos. O lock
// dura somente a transação SQL; crash libera o trabalho sem leases abandonados.
func (s *WinStore) RetryDueReference(ctx context.Context) (worked bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			worked = false
			err = openingStoreError(ctx, "retomar operação referenciada", err)
		}
	}()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer rollbackFinancial(tx)
	at := s.now()
	var id string
	var attempts int
	var deadline time.Time
	err = tx.QueryRow(ctx, `SELECT id,reference_attempts,reference_deadline FROM wager_transactions
  WHERE kind IN ('WIN','REFUND','ROLLBACK') AND status='PENDING_REFERENCE' AND next_reference_attempt_at<=$1
  ORDER BY next_reference_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, at).Scan(&id, &attempts, &deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	pending, err := loadReferenced(ctx, tx, id)
	if err != nil {
		return false, err
	}
	current, err := lockWinWallet(ctx, tx, pending.Request().BetRequest)
	if err != nil {
		return false, err
	}
	reference, err := findWinReference(ctx, tx, pending.Request())
	if err != nil {
		return false, err
	}
	if at.Before(pending.UpdatedAt()) {
		at = pending.UpdatedAt()
	}
	completed, moved, err := pending.Apply(current, reference, at)
	if err != nil {
		return false, err
	}
	attempts++
	if completed.Status() == wager.PendingReference && (attempts >= referenceMaxAttempts || !at.Before(deadline)) {
		completed, err = completed.ExpireReference(current, completed.UpdatedAt())
		if err != nil {
			return false, err
		}
	}
	var next any
	if completed.Status() == wager.PendingReference {
		delay := time.Second * time.Duration(1<<attempts)
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		next = completed.UpdatedAt().Add(delay)
	}
	_, err = tx.Exec(ctx, `UPDATE wager_transactions SET status=$2,failure_code=$3,reference_transaction_id=$4,
  balance_before_cents=$5,balance_after_cents=$6,wallet_version=$7,updated_at=$8,reference_attempts=$9,next_reference_attempt_at=$10 WHERE id=$1`,
		id, completed.Status(), nullableString(completed.FailureCode()), nullableString(completed.ReferenceID()), completed.BalanceBefore().MinorUnits(),
		completed.Balance().MinorUnits(), completed.WalletVersion(), completed.UpdatedAt(), attempts, next)
	if err != nil {
		return false, err
	}
	if err := saveReferencedEffects(ctx, tx, completed, current, moved, false); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
