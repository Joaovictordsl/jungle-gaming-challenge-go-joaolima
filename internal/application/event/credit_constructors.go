package event

import (
	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
)

func NewWinProcessed(w wager.Win) (WagerTransactionProcessed, error) {
	if w.Kind() != wager.WinKind {
		return WagerTransactionProcessed{}, ErrInvalidEvent
	}
	return NewReferencedProcessed(w)
}
func NewWinBalanceChanged(w wager.Win, e ledger.Entry) (WalletBalanceChanged, error) {
	if w.Kind() != wager.WinKind {
		return WalletBalanceChanged{}, ErrInvalidEvent
	}
	return NewReferencedBalanceChanged(w, e)
}
func NewWinRejected(w wager.Win) (WagerTransactionRejected, error) {
	if w.Kind() != wager.WinKind {
		return WagerTransactionRejected{}, ErrInvalidEvent
	}
	return NewReferencedRejected(w)
}
func NewWinPendingReference(w wager.Win) (WagerTransactionPendingReference, error) {
	if w.Kind() != wager.WinKind {
		return WagerTransactionPendingReference{}, ErrInvalidEvent
	}
	return NewReferencedPendingReference(w)
}
func NewRefundProcessed(r wager.Refund) (WagerTransactionProcessed, error) {
	if r.Kind() != wager.RefundKind {
		return WagerTransactionProcessed{}, ErrInvalidEvent
	}
	return NewReferencedProcessed(r)
}
func NewRefundBalanceChanged(r wager.Refund, e ledger.Entry) (WalletBalanceChanged, error) {
	if r.Kind() != wager.RefundKind {
		return WalletBalanceChanged{}, ErrInvalidEvent
	}
	return NewReferencedBalanceChanged(r, e)
}
func NewRefundRejected(r wager.Refund) (WagerTransactionRejected, error) {
	if r.Kind() != wager.RefundKind {
		return WagerTransactionRejected{}, ErrInvalidEvent
	}
	return NewReferencedRejected(r)
}
func NewRefundPendingReference(r wager.Refund) (WagerTransactionPendingReference, error) {
	if r.Kind() != wager.RefundKind {
		return WagerTransactionPendingReference{}, ErrInvalidEvent
	}
	return NewReferencedPendingReference(r)
}

func NewRollbackProcessed(r wager.Rollback) (WagerTransactionProcessed, error) {
	if r.Kind() != wager.RollbackKind {
		return WagerTransactionProcessed{}, ErrInvalidEvent
	}
	return NewReferencedProcessed(r)
}
func NewRollbackBalanceChanged(r wager.Rollback, e ledger.Entry) (WalletBalanceChanged, error) {
	if r.Kind() != wager.RollbackKind {
		return WalletBalanceChanged{}, ErrInvalidEvent
	}
	return NewReferencedBalanceChanged(r, e)
}
func NewRollbackRejected(r wager.Rollback) (WagerTransactionRejected, error) {
	if r.Kind() != wager.RollbackKind {
		return WagerTransactionRejected{}, ErrInvalidEvent
	}
	return NewReferencedRejected(r)
}
func NewRollbackPendingReference(r wager.Rollback) (WagerTransactionPendingReference, error) {
	if r.Kind() != wager.RollbackKind {
		return WagerTransactionPendingReference{}, ErrInvalidEvent
	}
	return NewReferencedPendingReference(r)
}
