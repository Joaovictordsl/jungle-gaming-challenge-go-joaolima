package postgres

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	appwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

type failedTransaction interface {
	ID() string
	Status() string
	FailureCode() string
	Balance() money.Money
	WalletVersion() int64
	CreatedAt() time.Time
	UpdatedAt() time.Time
}

func financialResult(t failedTransaction, replay bool) appwager.Result {
	return appwager.Result{TransactionID: t.ID(), Status: t.Status(), FailureCode: t.FailureCode(), Balance: t.Balance(), WalletVersion: t.WalletVersion(), FinancialReplay: replay}
}

// O chamador já desfez o savepoint financeiro. A auditoria e a inbox são
// confirmadas juntas, sem ledger, evento de rejeição ou alteração de carteira.
func recordFailedTransaction(ctx context.Context, tx pgx.Tx, c appwager.Command, at time.Time) (appwager.Result, error) {
	hash, err := c.PayloadHash()
	if err != nil {
		return appwager.Result{}, err
	}
	if result, found, err := findCommandReplay(ctx, tx, c, hash); err != nil || found {
		return result, err
	}
	current, err := lockWinWallet(ctx, tx, c.BetRequest)
	if err != nil {
		return appwager.Result{}, err
	}
	if result, found, err := findCommandReplay(ctx, tx, c, hash); err != nil || found {
		return result, err
	}
	id := rand.Text()
	var completed failedTransaction
	switch c.Kind {
	case wager.BetKind:
		pending, e := wager.NewBet(id, c.BetRequest, at)
		if e != nil {
			return appwager.Result{}, e
		}
		completed, err = pending.FailInfrastructure(current, at)
	case wager.LossKind:
		pending, e := wager.NewLoss(id, wager.LossRequest{BetRequest: c.BetRequest}, at)
		if e != nil {
			return appwager.Result{}, e
		}
		completed, err = pending.FailInfrastructure(current, at)
	default:
		r := wager.WinRequest{BetRequest: c.BetRequest, ReferenceExternalTransactionID: c.ReferenceExternalTransactionID}
		var pending wager.ReferencedTransaction
		switch c.Kind {
		case wager.WinKind:
			pending, err = wager.NewWin(id, r, at)
		case wager.RefundKind:
			pending, err = wager.NewRefund(id, wager.RefundRequest{WinRequest: r}, at)
		case wager.RollbackKind:
			pending, err = wager.NewRollback(id, wager.RollbackRequest{WinRequest: r}, at)
		default:
			return appwager.Result{}, wager.ErrInvalidState
		}
		if err != nil {
			return appwager.Result{}, err
		}
		completed, err = pending.FailInfrastructure(current, at)
	}
	if err != nil {
		return appwager.Result{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO wager_transactions
 (id,origin,kind,wallet_id,player_id,currency,amount_cents,status,balance_before_cents,balance_after_cents,wallet_version,
 created_at,updated_at,provider_id,external_transaction_id,idempotency_key,payload_hash,round_id,game_id,failure_code,reference_external_transaction_id)
 VALUES($1,'EXTERNAL',$2,$3,$4,$5,$6,'FAILED',$7,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
 ON CONFLICT DO NOTHING`, completed.ID(), c.Kind, c.WalletID, c.PlayerID, c.Money.Currency(), c.Money.MinorUnits(),
		completed.Balance().MinorUnits(), completed.WalletVersion(), completed.CreatedAt(), completed.UpdatedAt(), c.ProviderID,
		c.ExternalTransactionID, c.IdempotencyKey, hash, c.RoundID, c.GameID, completed.FailureCode(), nullableString(c.ReferenceExternalTransactionID))
	if err != nil {
		return appwager.Result{}, err
	}
	if tag.RowsAffected() == 0 {
		if result, found, err := findCommandReplay(ctx, tx, c, hash); err != nil || found {
			return result, err
		}
		return appwager.Result{}, fmt.Errorf("colisão inesperada de identidade financeira")
	}
	return financialResult(completed, false), nil
}

func findCommandReplay(ctx context.Context, tx pgx.Tx, c appwager.Command, hash string) (appwager.Result, bool, error) {
	switch c.Kind {
	case wager.BetKind:
		t, found, err := findBetReplay(ctx, tx, c.BetRequest, hash)
		if err != nil || !found {
			return appwager.Result{}, found, err
		}
		return financialResult(t, true), true, nil
	case wager.LossKind:
		t, found, err := findLossReplay(ctx, tx, wager.LossRequest{BetRequest: c.BetRequest}, hash)
		if err != nil || !found {
			return appwager.Result{}, found, err
		}
		return financialResult(t, true), true, nil
	default:
		t, found, err := findReferencedReplay(ctx, tx, wager.WinRequest{BetRequest: c.BetRequest, ReferenceExternalTransactionID: c.ReferenceExternalTransactionID}, hash)
		if err != nil || !found {
			return appwager.Result{}, found, err
		}
		return financialResult(t, true), true, nil
	}
}
