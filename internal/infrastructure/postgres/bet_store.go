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

var _ applicationwager.BetStore = (*BetStore)(nil)

type BetStore struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
}

func NewBetStore(pool *pgxpool.Pool) *BetStore { return &BetStore{pool: pool} }

func (s *BetStore) ProcessBet(ctx context.Context, pending wager.Bet) (result wager.Bet, replay bool, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	if err = pending.Validate(); err != nil {
		return
	}
	if pending.Status() != wager.Pending {
		return wager.Bet{}, false, wager.ErrInvalidState
	}
	// Prazo cobre pool, espera por lock, consultas e commit, inclusive em chamadas sem deadline.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			result = wager.Bet{}
			replay = false
			err = openingStoreError(ctx, "processar BET", err)
		}
	}()
	tx := s.tx
	owned := tx == nil
	if owned {
		tx, err = s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return wager.Bet{}, false, err
		}
	}
	if owned {
		defer rollbackFinancial(tx)
	}
	input := pending.Request()
	hash, err := input.PayloadHash()
	if err != nil {
		return wager.Bet{}, false, err
	}
	if existing, found, lookupErr := findBetReplay(ctx, tx, input, hash); lookupErr != nil || found {
		return existing, found, lookupErr
	}

	// Coordenação no PostgreSQL, por carteira; não há mutex ou lock global.
	var lockedID string
	err = tx.QueryRow(ctx, `SELECT id FROM wallets WHERE id=$1 FOR UPDATE`, input.WalletID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return wager.Bet{}, false, wallet.ErrNotFound
	}
	if err != nil {
		return wager.Bet{}, false, err
	}
	current, err := NewWalletRepository(tx).FindByID(ctx, lockedID)
	if err != nil {
		return wager.Bet{}, false, err
	}
	// Outra instância pode ter confirmado a mesma operação enquanto este lock esperava.
	if existing, found, lookupErr := findBetReplay(ctx, tx, input, hash); lookupErr != nil || found {
		return existing, found, lookupErr
	}
	completed, moved, err := pending.Apply(current, pending.CreatedAt())
	if err != nil {
		return wager.Bet{}, false, err
	}
	var failure any
	if completed.FailureCode() != "" {
		failure = completed.FailureCode()
	}
	tag, err := tx.Exec(ctx, `INSERT INTO wager_transactions
  (id,origin,kind,wallet_id,player_id,currency,amount_cents,status,balance_after_cents,wallet_version,
   created_at,updated_at,provider_id,external_transaction_id,idempotency_key,payload_hash,round_id,game_id,balance_before_cents,failure_code)
  VALUES ($1,'EXTERNAL','BET',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
  ON CONFLICT DO NOTHING`, completed.ID(), input.WalletID, input.PlayerID, input.Money.Currency(), input.Money.MinorUnits(),
		completed.Status(), completed.Balance().MinorUnits(), completed.WalletVersion(), completed.CreatedAt(), completed.UpdatedAt(),
		input.ProviderID, input.ExternalTransactionID, input.IdempotencyKey, hash, input.RoundID, input.GameID, completed.BalanceBefore().MinorUnits(), failure)
	if err != nil {
		return wager.Bet{}, false, err
	}
	if tag.RowsAffected() == 0 {
		// Inclui colisões da mesma chave/ID externo em carteiras diferentes. O INSERT
		// aguarda a constraint única e a próxima consulta vê o commit vencedor.
		existing, found, err := findBetReplay(ctx, tx, input, hash)
		if err != nil || found {
			return existing, found, err
		}
		return wager.Bet{}, false, fmt.Errorf("colisão inesperada de identidade financeira")
	}

	var snapshots []event.Snapshot
	if completed.Status() == wager.Processed {
		entry, err := ledger.New("ledger:"+completed.ID(), input.WalletID, completed.ID(), ledger.Debit, input.Money, current.Balance(), moved.Balance(), completed.UpdatedAt())
		if err != nil {
			return wager.Bet{}, false, err
		}
		tag, err := tx.Exec(ctx, `UPDATE wallets SET balance_cents=$2,version=$3,updated_at=$4 WHERE id=$1 AND version=$5`,
			moved.ID(), moved.Balance().MinorUnits(), moved.Version(), moved.UpdatedAt(), current.Version())
		if err != nil {
			return wager.Bet{}, false, err
		}
		if tag.RowsAffected() != 1 {
			return wager.Bet{}, false, fmt.Errorf("versão inesperada sob lock da carteira")
		}
		_, err = tx.Exec(ctx, `INSERT INTO wallet_ledger_entries
   (id,wallet_id,transaction_id,direction,currency,amount_cents,balance_before_cents,balance_after_cents,created_at)
   VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, entry.ID(), entry.WalletID(), entry.TransactionID(), entry.Direction(), entry.Amount().Currency(),
			entry.Amount().MinorUnits(), entry.BalanceBefore().MinorUnits(), entry.BalanceAfter().MinorUnits(), entry.CreatedAt())
		if err != nil {
			return wager.Bet{}, false, err
		}
		processed, err := event.NewBetProcessed(completed)
		if err != nil {
			return wager.Bet{}, false, err
		}
		changed, err := event.NewBetBalanceChanged(completed, entry)
		if err != nil {
			return wager.Bet{}, false, err
		}
		snapshots = []event.Snapshot{processed, changed}
	} else {
		rejected, err := event.NewBetRejected(completed)
		if err != nil {
			return wager.Bet{}, false, err
		}
		snapshots = []event.Snapshot{rejected}
	}
	for _, snapshot := range snapshots {
		payload, err := json.Marshal(snapshot)
		if err != nil {
			return wager.Bet{}, false, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO outbox_events (id,transaction_id,aggregate_id,event_type,payload,occurred_at,next_attempt_at)
   VALUES ($1,$2,$3,$4,$5,$6,$6)`, snapshot.EventID(), completed.ID(), snapshot.AggregateID(), snapshot.EventType(), payload, snapshot.OccurredAt())
		if err != nil {
			return wager.Bet{}, false, err
		}
	}
	if owned {
		if err = tx.Commit(ctx); err != nil {
			return wager.Bet{}, false, err
		}
	}
	return completed, false, nil
}

func findBetReplay(ctx context.Context, tx pgx.Tx, input wager.BetRequest, hash string) (wager.Bet, bool, error) {
	rows, err := tx.Query(ctx, `SELECT id,provider_id,external_transaction_id,idempotency_key,payload_hash,wallet_id,player_id,
  round_id,game_id,currency,amount_cents,status,COALESCE(failure_code,''),balance_before_cents,balance_after_cents,
  wallet_version,created_at,updated_at FROM wager_transactions
		WHERE provider_id=$1 AND (idempotency_key=$2 OR external_transaction_id=$3)
		ORDER BY (idempotency_key=$2) DESC`, input.ProviderID, input.IdempotencyKey, input.ExternalTransactionID)
	if err != nil {
		return wager.Bet{}, false, err
	}
	defer rows.Close()
	var found wager.Bet
	count := 0
	for rows.Next() {
		var r wager.BetRequest
		var id, storedHash, currency, status, failure string
		var cents, before, after, version int64
		var created, updated time.Time
		if err := rows.Scan(&id, &r.ProviderID, &r.ExternalTransactionID, &r.IdempotencyKey, &storedHash, &r.WalletID, &r.PlayerID,
			&r.RoundID, &r.GameID, &currency, &cents, &status, &failure, &before, &after, &version, &created, &updated); err != nil {
			return wager.Bet{}, false, err
		}
		if r.IdempotencyKey == input.IdempotencyKey && storedHash != hash {
			return wager.Bet{}, false, wager.ErrIdempotencyConflict
		}
		if r.IdempotencyKey != input.IdempotencyKey {
			return wager.Bet{}, false, wager.ErrExternalConflict
		}
		r.Money, err = money.FromMinorUnits(cents, currency)
		if err != nil {
			return wager.Bet{}, false, err
		}
		computed, err := r.PayloadHash()
		if err != nil || computed != storedHash {
			return wager.Bet{}, false, fmt.Errorf("hash persistido incoerente")
		}
		beforeMoney, err := money.FromMinorUnits(before, currency)
		if err != nil {
			return wager.Bet{}, false, err
		}
		afterMoney, err := money.FromMinorUnits(after, currency)
		if err != nil {
			return wager.Bet{}, false, err
		}
		found, err = wager.RehydrateBet(id, r, status, failure, beforeMoney, afterMoney, version, created, updated)
		if err != nil {
			return wager.Bet{}, false, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return wager.Bet{}, false, err
	}
	return found, count == 1, nil
}
