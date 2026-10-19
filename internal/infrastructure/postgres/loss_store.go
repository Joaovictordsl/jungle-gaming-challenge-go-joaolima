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
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

var _ applicationwager.LossStore = (*LossStore)(nil)

type LossStore struct{ pool *pgxpool.Pool }

func NewLossStore(pool *pgxpool.Pool) *LossStore { return &LossStore{pool: pool} }

func (s *LossStore) ProcessLoss(ctx context.Context, pending wager.Loss) (result wager.Loss, replay bool, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	if err = pending.Validate(); err != nil {
		return
	}
	if pending.Status() != wager.Pending {
		return wager.Loss{}, false, wager.ErrInvalidState
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			result = wager.Loss{}
			replay = false
			err = openingStoreError(ctx, "processar LOSS", err)
		}
	}()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return wager.Loss{}, false, err
	}
	defer rollbackFinancial(tx)
	input := pending.Request()
	hash, err := input.PayloadHash()
	if err != nil {
		return wager.Loss{}, false, err
	}
	if existing, found, err := findLossReplay(ctx, tx, input, hash); err != nil || found {
		return existing, found, err
	}
	current, err := lockWinWallet(ctx, tx, input.BetRequest)
	if err != nil {
		return wager.Loss{}, false, err
	}
	if existing, found, err := findLossReplay(ctx, tx, input, hash); err != nil || found {
		return existing, found, err
	}
	completed, err := pending.Process(current, pending.CreatedAt())
	if err != nil {
		return wager.Loss{}, false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO wager_transactions
  (id,origin,kind,wallet_id,player_id,currency,amount_cents,status,balance_after_cents,wallet_version,
   created_at,updated_at,provider_id,external_transaction_id,idempotency_key,payload_hash,round_id,game_id,balance_before_cents)
  VALUES ($1,'EXTERNAL','LOSS',$2,$3,$4,0,'PROCESSED',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$5)
  ON CONFLICT DO NOTHING`, completed.ID(), input.WalletID, input.PlayerID, input.Money.Currency(), completed.Balance().MinorUnits(), completed.WalletVersion(),
		completed.CreatedAt(), completed.UpdatedAt(), input.ProviderID, input.ExternalTransactionID, input.IdempotencyKey, hash, input.RoundID, input.GameID)
	if err != nil {
		return wager.Loss{}, false, err
	}
	if tag.RowsAffected() == 0 {
		existing, found, err := findLossReplay(ctx, tx, input, hash)
		if err != nil || found {
			return existing, found, err
		}
		return wager.Loss{}, false, fmt.Errorf("colisão inesperada de identidade financeira")
	}
	snapshot, err := event.NewLossProcessed(completed)
	if err != nil {
		return wager.Loss{}, false, err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return wager.Loss{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(id,transaction_id,aggregate_id,event_type,payload,occurred_at,next_attempt_at)
  VALUES ($1,$2,$3,$4,$5,$6,$6)`, snapshot.EventID(), completed.ID(), snapshot.AggregateID(), snapshot.EventType(), payload, snapshot.OccurredAt())
	if err != nil {
		return wager.Loss{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return wager.Loss{}, false, err
	}
	return completed, false, nil
}

func findLossReplay(ctx context.Context, tx pgx.Tx, input wager.LossRequest, hash string) (wager.Loss, bool, error) {
	var id, key, storedHash string
	err := tx.QueryRow(ctx, `SELECT id,idempotency_key,payload_hash FROM wager_transactions
  WHERE provider_id=$1 AND (idempotency_key=$2 OR external_transaction_id=$3)
  ORDER BY (idempotency_key=$2) DESC LIMIT 1`, input.ProviderID, input.IdempotencyKey, input.ExternalTransactionID).Scan(&id, &key, &storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return wager.Loss{}, false, nil
	}
	if err != nil {
		return wager.Loss{}, false, err
	}
	if key == input.IdempotencyKey && storedHash != hash {
		return wager.Loss{}, false, wager.ErrIdempotencyConflict
	}
	if key != input.IdempotencyKey {
		return wager.Loss{}, false, wager.ErrExternalConflict
	}
	var r wager.LossRequest
	var currency, status string
	var cents, balance, version int64
	var created, updated time.Time
	err = tx.QueryRow(ctx, `SELECT provider_id,external_transaction_id,idempotency_key,wallet_id,player_id,round_id,game_id,
  currency,amount_cents,status,balance_after_cents,wallet_version,created_at,updated_at FROM wager_transactions WHERE id=$1 AND kind='LOSS'`, id).
		Scan(&r.ProviderID, &r.ExternalTransactionID, &r.IdempotencyKey, &r.WalletID, &r.PlayerID, &r.RoundID, &r.GameID, &currency, &cents, &status, &balance, &version, &created, &updated)
	if err != nil {
		return wager.Loss{}, false, err
	}
	if status != wager.Processed {
		return wager.Loss{}, false, wager.ErrInvalidState
	}
	r.Money, err = money.FromMinorUnits(cents, currency)
	if err != nil {
		return wager.Loss{}, false, err
	}
	computed, err := r.PayloadHash()
	if err != nil || computed != storedHash {
		return wager.Loss{}, false, fmt.Errorf("hash persistido incoerente")
	}
	observed, err := money.FromMinorUnits(balance, currency)
	if err != nil {
		return wager.Loss{}, false, err
	}
	result, err := wager.RehydrateLoss(id, r, observed, version, created, updated)
	return result, err == nil, err
}
