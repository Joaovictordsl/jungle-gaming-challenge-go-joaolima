package wager

import (
	"errors"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

const RefundKind = "REFUND"
const ReferenceAmountMismatch = "REFERENCE_AMOUNT_MISMATCH"
const AlreadyReversed = "ALREADY_REVERSED"

var ErrInvalidRefund = errors.New("dados de devolução inválidos")

type RefundRequest struct{ WinRequest }

func (r RefundRequest) Validate() error {
	if err := r.WinRequest.Validate(); err != nil {
		return errors.Join(ErrInvalidRefund, err)
	}
	if r.ReferenceExternalTransactionID == "" {
		return ErrInvalidRefund
	}
	return nil
}
func (r RefundRequest) PayloadHash() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	return r.WinRequest.payloadHash(RefundKind)
}
func NewRefund(id string, r RefundRequest, at time.Time) (Refund, error) {
	refund := Refund{id: id, kind: RefundKind, request: r.WinRequest, status: Pending, createdAt: at.UTC(), updatedAt: at.UTC()}
	if err := refund.Validate(); err != nil {
		return Refund{}, err
	}
	return refund, nil
}
func RehydrateRefund(id string, r RefundRequest, status, failure, referenceID string, before, balance money.Money, version int64, created, updated time.Time) (Refund, error) {
	if status == Pending {
		return Refund{}, ErrInvalidState
	}
	refund := Refund{id: id, kind: RefundKind, request: r.WinRequest, status: status, failureCode: failure, referenceID: referenceID,
		before: before, balance: balance, walletVersion: version, createdAt: created.UTC(), updatedAt: updated.UTC()}
	if err := refund.Validate(); err != nil {
		return Refund{}, err
	}
	return refund, nil
}
