package wager

import (
	"errors"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"time"
)

const RollbackKind = "ROLLBACK"
const ReversalInsufficientFunds = "REVERSAL_INSUFFICIENT_FUNDS"

var ErrInvalidRollback = errors.New("dados de estorno inválidos")

type RollbackRequest struct{ WinRequest }

func (r RollbackRequest) Validate() error {
	if err := r.WinRequest.Validate(); err != nil {
		return errors.Join(ErrInvalidRollback, err)
	}
	if r.ReferenceExternalTransactionID == "" {
		return ErrInvalidRollback
	}
	return nil
}
func (r RollbackRequest) PayloadHash() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	return r.WinRequest.payloadHash(RollbackKind)
}
func NewRollback(id string, r RollbackRequest, at time.Time) (Rollback, error) {
	result := Rollback{id: id, kind: RollbackKind, request: r.WinRequest, status: Pending, createdAt: at.UTC(), updatedAt: at.UTC()}
	if err := result.Validate(); err != nil {
		return Rollback{}, err
	}
	return result, nil
}
func RehydrateRollback(id string, r RollbackRequest, status, failure, referenceID, direction string, before, balance money.Money, version int64, created, updated time.Time) (Rollback, error) {
	if status == Pending {
		return Rollback{}, ErrInvalidState
	}
	result := Rollback{id: id, kind: RollbackKind, request: r.WinRequest, status: status, failureCode: failure, referenceID: referenceID, direction: direction, before: before, balance: balance, walletVersion: version, createdAt: created.UTC(), updatedAt: updated.UTC()}
	if err := result.Validate(); err != nil {
		return Rollback{}, err
	}
	return result, nil
}
