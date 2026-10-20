package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-challenge-go-joaolima/internal/application/message"
	appwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

var _ message.Inbox = (*InboxStore)(nil)

type InboxStore struct{ pool *pgxpool.Pool }

func NewInboxStore(pool *pgxpool.Pool) *InboxStore { return &InboxStore{pool: pool} }

type inboxStores struct {
	bet    *BetStore
	loss   *LossStore
	credit *WinStore
}

func (s inboxStores) ProcessBet(ctx context.Context, t wager.Bet) (wager.Bet, bool, error) {
	return s.bet.ProcessBet(ctx, t)
}
func (s inboxStores) ProcessLoss(ctx context.Context, t wager.Loss) (wager.Loss, bool, error) {
	return s.loss.ProcessLoss(ctx, t)
}
func (s inboxStores) ProcessWin(ctx context.Context, t wager.Win) (wager.Win, bool, error) {
	return s.credit.ProcessWin(ctx, t)
}
func (s inboxStores) ProcessRefund(ctx context.Context, t wager.Refund) (wager.Refund, bool, error) {
	return s.credit.ProcessRefund(ctx, t)
}
func (s inboxStores) ProcessRollback(ctx context.Context, t wager.Rollback) (wager.Rollback, bool, error) {
	return s.credit.ProcessRollback(ctx, t)
}

func (s *InboxStore) Process(ctx context.Context, consumer string, e message.Envelope, finalAttempt bool, handle message.Handler) (result appwager.Result, replay bool, err error) {
	hash, err := e.Hash()
	if err != nil {
		return result, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			err = openingStoreError(ctx, "processar inbox", err)
		}
	}()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, false, err
	}
	defer rollbackFinancial(tx)
	at := time.Now().UTC().Truncate(time.Microsecond)
	tag, err := tx.Exec(ctx, `INSERT INTO inbox_messages(consumer_name,message_id,payload_hash,provider_id,external_transaction_id,wallet_id,occurred_at,received_at,status)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,'PENDING') ON CONFLICT DO NOTHING`, consumer, e.MessageID, hash, e.Command.ProviderID, e.Command.ExternalTransactionID, e.Command.WalletID, e.OccurredAt, at)
	if err != nil {
		return result, false, err
	}
	if tag.RowsAffected() == 0 {
		return loadInbox(ctx, tx, consumer, e.MessageID, hash)
	}
	// O savepoint permite auditar uma falha permanente sem confirmar efeitos
	// financeiros parciais. A transação externa ainda possui a claim da inbox.
	work, err := tx.Begin(ctx)
	if err != nil {
		return result, false, err
	}
	stores := inboxStores{bet: &BetStore{tx: work}, loss: &LossStore{tx: work}, credit: &WinStore{tx: work}}
	result, err = handle(ctx, stores)
	code := ""
	if err != nil {
		code = message.PermanentCode(err)
		result = appwager.Result{}
		rollbackFinancial(work)
		if code == "" && !finalAttempt {
			return appwager.Result{}, false, err
		}
		if code == "" {
			// Somente falha de infraestrutura esgotada reserva a identidade
			// financeira. Conflitos e entrada inválida continuam na auditoria da inbox.
			result, err = recordFailedTransaction(ctx, tx, e.Command, at)
			if err != nil {
				code = message.PermanentCode(err)
				if code == "" {
					return appwager.Result{}, false, err
				}
			}
		}
	} else if err = work.Commit(ctx); err != nil {
		return appwager.Result{}, false, err
	}
	if result.Status == wager.Failed {
		code = "attempts_exhausted"
	}
	if code != "" {
		if result.TransactionID == "" {
			_, err = tx.Exec(ctx, `UPDATE inbox_messages SET status='FAILED',failure_code=$3,completed_at=GREATEST(received_at,clock_timestamp()) WHERE consumer_name=$1 AND message_id=$2`, consumer, e.MessageID, code)
		} else {
			_, err = tx.Exec(ctx, `UPDATE inbox_messages SET status='FAILED',failure_code=$10,transaction_id=$3,result_status=$4,result_failure_code=$5,balance_cents=$6,currency=$7,wallet_version=$8,financial_replay=$9,completed_at=GREATEST(received_at,clock_timestamp()) WHERE consumer_name=$1 AND message_id=$2`, consumer, e.MessageID, result.TransactionID, result.Status, nullableString(result.FailureCode), result.Balance.MinorUnits(), result.Balance.Currency(), result.WalletVersion, result.FinancialReplay, code)
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE inbox_messages SET status='COMPLETED',transaction_id=$3,result_status=$4,result_failure_code=$5,balance_cents=$6,currency=$7,wallet_version=$8,financial_replay=$9,completed_at=GREATEST(received_at,clock_timestamp()) WHERE consumer_name=$1 AND message_id=$2`, consumer, e.MessageID, result.TransactionID, result.Status, nullableString(result.FailureCode), result.Balance.MinorUnits(), result.Balance.Currency(), result.WalletVersion, result.FinancialReplay)
	}
	if err != nil {
		return appwager.Result{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return appwager.Result{}, false, err
	}
	if code != "" {
		return result, false, &message.Failure{Code: code}
	}
	return result, false, nil
}

func loadInbox(ctx context.Context, tx pgx.Tx, consumer, id, hash string) (result appwager.Result, replay bool, err error) {
	var storedHash, status, code, transactionID string
	var cents, version int64
	var currency string
	err = tx.QueryRow(ctx, `SELECT payload_hash,status,COALESCE(failure_code,''),COALESCE(transaction_id,'') FROM inbox_messages WHERE consumer_name=$1 AND message_id=$2`, consumer, id).Scan(&storedHash, &status, &code, &transactionID)
	if err != nil {
		return result, false, err
	}
	if storedHash != hash {
		return result, false, message.ErrInboxConflict
	}
	if status == "FAILED" && transactionID == "" {
		return result, true, &message.Failure{Code: code}
	}
	if status != "COMPLETED" && status != "FAILED" {
		return result, false, errors.New("inbox confirmada incompleta")
	}
	err = tx.QueryRow(ctx, `SELECT transaction_id,result_status,COALESCE(result_failure_code,''),balance_cents,currency,wallet_version,financial_replay FROM inbox_messages WHERE consumer_name=$1 AND message_id=$2`, consumer, id).Scan(&result.TransactionID, &result.Status, &result.FailureCode, &cents, &currency, &version, &result.FinancialReplay)
	if err != nil {
		return result, false, err
	}
	result.WalletVersion = version
	result.Balance, err = money.FromMinorUnits(cents, currency)
	if err == nil && status == "FAILED" {
		return result, true, &message.Failure{Code: code}
	}
	return result, err == nil, err
}
