package wager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

const LossKind = "LOSS"

var ErrInvalidLoss = errors.New("dados de LOSS inválidos")

// LossRequest compartilha os metadados, mas exige zero em vez do valor positivo de BET.
type LossRequest struct{ BetRequest }

func (r LossRequest) Validate() error {
	if err := r.BetRequest.validateFields(); err != nil {
		return errors.Join(ErrInvalidLoss, err)
	}
	if r.Money.MinorUnits() != 0 {
		return ErrInvalidLoss
	}
	return nil
}
func (r LossRequest) PayloadHash() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{"providerId": r.ProviderID, "externalTransactionId": r.ExternalTransactionID,
		"walletId": r.WalletID, "playerId": r.PlayerID, "roundId": r.RoundID, "gameId": r.GameID, "kind": LossKind,
		"money": map[string]string{"amount": "0.00", "currency": r.Money.Currency()}})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

type Loss struct {
	id                   string
	request              LossRequest
	status               string
	balance              money.Money
	walletVersion        int64
	createdAt, updatedAt time.Time
}

func NewLoss(id string, r LossRequest, at time.Time) (Loss, error) {
	l := Loss{id: id, request: r, status: Pending, createdAt: at.UTC(), updatedAt: at.UTC()}
	if err := l.Validate(); err != nil {
		return Loss{}, err
	}
	return l, nil
}
func RehydrateLoss(id string, r LossRequest, balance money.Money, version int64, created, updated time.Time) (Loss, error) {
	l := Loss{id: id, request: r, status: Processed, balance: balance, walletVersion: version, createdAt: created.UTC(), updatedAt: updated.UTC()}
	if err := l.Validate(); err != nil {
		return Loss{}, err
	}
	return l, nil
}
func (l Loss) Validate() error {
	if strings.TrimSpace(l.id) == "" || l.createdAt.IsZero() || l.updatedAt.Before(l.createdAt) {
		return ErrInvalidLoss
	}
	if err := l.request.Validate(); err != nil {
		return err
	}
	switch l.status {
	case Pending:
		if l.balance != (money.Money{}) || l.walletVersion != 0 {
			return ErrInvalidLoss
		}
	case Processed:
		if err := l.balance.Validate(); err != nil {
			return err
		}
		if l.balance.Currency() != l.request.Money.Currency() || l.balance.MinorUnits() < 0 || l.walletVersion < 1 {
			return ErrInvalidLoss
		}
	default:
		return ErrInvalidState
	}
	return nil
}

// Process registra o saldo observado, sem chamar Debit/Credit ou modificar a carteira.
func (l Loss) Process(current wallet.Wallet, at time.Time) (Loss, error) {
	if err := l.Validate(); err != nil {
		return Loss{}, err
	}
	if l.status != Pending {
		return Loss{}, ErrInvalidState
	}
	if err := current.Validate(); err != nil {
		return Loss{}, err
	}
	if current.ID() != l.request.WalletID || current.PlayerID() != l.request.PlayerID || current.Balance().Currency() != l.request.Money.Currency() {
		return Loss{}, ErrWalletMismatch
	}
	if at.IsZero() || at.Before(l.createdAt) {
		return Loss{}, ErrInvalidLoss
	}
	if at.Before(current.UpdatedAt()) {
		at = current.UpdatedAt()
	}
	l.status, l.balance, l.walletVersion, l.updatedAt = Processed, current.Balance(), current.Version(), at.UTC()
	return l, l.Validate()
}
func (l Loss) ID() string           { return l.id }
func (l Loss) Request() LossRequest { return l.request }
func (l Loss) Status() string       { return l.status }
func (l Loss) Balance() money.Money { return l.balance }
func (l Loss) WalletVersion() int64 { return l.walletVersion }
func (l Loss) CreatedAt() time.Time { return l.createdAt }
func (l Loss) UpdatedAt() time.Time { return l.updatedAt }
