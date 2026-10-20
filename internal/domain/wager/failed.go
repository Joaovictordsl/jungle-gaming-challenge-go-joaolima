package wager

import (
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

const Failed = "FAILED"
const InfrastructureRetriesExhausted = "INFRASTRUCTURE_RETRIES_EXHAUSTED"

// A infraestrutura chama esta transição apenas depois de desfazer a tentativa
// financeira e esgotar suas retentativas. Não é uma rejeição de negócio.
func (b Bet) FailInfrastructure(current wallet.Wallet, at time.Time) (Bet, error) {
	if err := b.Validate(); err != nil {
		return Bet{}, err
	}
	if b.status != Pending {
		return Bet{}, ErrInvalidState
	}
	at, err := failureObservation(b.request, current, b.createdAt, at)
	if err != nil {
		return Bet{}, err
	}
	b.status, b.failureCode = Failed, InfrastructureRetriesExhausted
	b.before, b.balance, b.walletVersion, b.updatedAt = current.Balance(), current.Balance(), current.Version(), at
	return b, b.Validate()
}

func (w ReferencedTransaction) FailInfrastructure(current wallet.Wallet, at time.Time) (ReferencedTransaction, error) {
	if w.status != Pending {
		return ReferencedTransaction{}, ErrInvalidState
	}
	w, err := w.observe(current, at)
	if err != nil {
		return ReferencedTransaction{}, err
	}
	w.status, w.failureCode = Failed, InfrastructureRetriesExhausted
	return w, w.Validate()
}

func (l Loss) FailInfrastructure(current wallet.Wallet, at time.Time) (Loss, error) {
	if err := l.Validate(); err != nil {
		return Loss{}, err
	}
	if l.status != Pending {
		return Loss{}, ErrInvalidState
	}
	at, err := failureObservation(l.request.BetRequest, current, l.createdAt, at)
	if err != nil {
		return Loss{}, err
	}
	l.status, l.failureCode = Failed, InfrastructureRetriesExhausted
	l.balance, l.walletVersion, l.updatedAt = current.Balance(), current.Version(), at
	return l, l.Validate()
}

func RehydrateFailedLoss(id string, r LossRequest, failure string, balance money.Money, version int64, created, updated time.Time) (Loss, error) {
	l := Loss{id: id, request: r, status: Failed, failureCode: failure, balance: balance, walletVersion: version, createdAt: created.UTC(), updatedAt: updated.UTC()}
	return l, l.Validate()
}

func failureObservation(r BetRequest, current wallet.Wallet, created, at time.Time) (time.Time, error) {
	if err := current.Validate(); err != nil {
		return time.Time{}, err
	}
	if current.ID() != r.WalletID || current.PlayerID() != r.PlayerID || current.Balance().Currency() != r.Money.Currency() {
		return time.Time{}, ErrWalletMismatch
	}
	if at.IsZero() || at.Before(created) {
		return time.Time{}, ErrInvalidState
	}
	if at.Before(current.UpdatedAt()) {
		at = current.UpdatedAt()
	}
	return at.UTC(), nil
}
