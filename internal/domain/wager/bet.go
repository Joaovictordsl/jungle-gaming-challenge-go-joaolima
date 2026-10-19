package wager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

const BetKind = "BET"
const Rejected = "REJECTED"
const InsufficientFunds = "INSUFFICIENT_FUNDS"

var (
	ErrInvalidBet          = errors.New("dados da aposta inválidos")
	ErrWalletMismatch      = errors.New("carteira incompatível com jogador ou moeda")
	ErrIdempotencyConflict = errors.New("chave de idempotência reutilizada com outro conteúdo")
	ErrExternalConflict    = errors.New("transação externa já registrada com outra chave")
)

// BetRequest é um valor de entrada. Bet guarda uma cópia validada e encapsulada.
type BetRequest struct {
	ProviderID, ExternalTransactionID, IdempotencyKey string
	WalletID, PlayerID, RoundID, GameID               string
	Money                                             money.Money
}

func (r BetRequest) Validate() error {
	if err := r.validateFields(); err != nil {
		return err
	}
	if r.Money.MinorUnits() <= 0 {
		return ErrInvalidBet
	}
	return nil
}

// Campos comuns às operações externas; a política de valor pertence a cada tipo.
func (r BetRequest) validateFields() error {
	for _, value := range []string{r.ProviderID, r.ExternalTransactionID, r.IdempotencyKey, r.WalletID, r.PlayerID, r.RoundID, r.GameID} {
		if strings.TrimSpace(value) == "" || len(value) > 256 || !utf8.ValidString(value) {
			return ErrInvalidBet
		}
	}
	if err := r.Money.Validate(); err != nil {
		return err
	}
	return nil
}

// PayloadHash usa SHA-256 de JSON com chaves ordenadas; exclui metadados de transporte
// e idempotencyKey. Money.Amount normaliza zeros à esquerda antes do hash.
func (r BetRequest) PayloadHash() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	amount, err := r.Money.Amount()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{
		"providerId": r.ProviderID, "externalTransactionId": r.ExternalTransactionID,
		"walletId": r.WalletID, "playerId": r.PlayerID, "roundId": r.RoundID, "gameId": r.GameID,
		"kind": BetKind, "money": map[string]string{"amount": amount, "currency": r.Money.Currency()},
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

type Bet struct {
	id                   string
	request              BetRequest
	status, failureCode  string
	before, balance      money.Money
	walletVersion        int64
	createdAt, updatedAt time.Time
}

func NewBet(id string, request BetRequest, at time.Time) (Bet, error) {
	b := Bet{id: id, request: request, status: Pending, createdAt: at.UTC(), updatedAt: at.UTC()}
	if err := b.Validate(); err != nil {
		return Bet{}, err
	}
	return b, nil
}

// RehydrateBet restaura um resultado terminal sem reaplicar o débito.
func RehydrateBet(id string, request BetRequest, status, failure string, before, balance money.Money, version int64, created, updated time.Time) (Bet, error) {
	b := Bet{id: id, request: request, status: status, failureCode: failure, before: before, balance: balance, walletVersion: version, createdAt: created.UTC(), updatedAt: updated.UTC()}
	if status != Processed && status != Rejected {
		return Bet{}, ErrInvalidState
	}
	if err := b.Validate(); err != nil {
		return Bet{}, err
	}
	return b, nil
}

func (b Bet) Validate() error {
	if strings.TrimSpace(b.id) == "" || b.createdAt.IsZero() || b.updatedAt.Before(b.createdAt) {
		return ErrInvalidBet
	}
	if err := b.request.Validate(); err != nil {
		return err
	}
	if b.status == Pending {
		if b.failureCode != "" || b.walletVersion != 0 || b.before != (money.Money{}) || b.balance != (money.Money{}) {
			return ErrInvalidBet
		}
		return nil
	}
	if b.status != Processed && b.status != Rejected {
		return ErrInvalidState
	}
	for _, m := range []money.Money{b.before, b.balance} {
		if err := m.Validate(); err != nil {
			return err
		}
		if m.Currency() != b.request.Money.Currency() || m.MinorUnits() < 0 {
			return ErrInvalidBet
		}
	}
	if b.walletVersion < 1 {
		return ErrInvalidBet
	}
	if b.status == Rejected {
		if b.failureCode != InsufficientFunds || b.before != b.balance || b.request.Money.MinorUnits() <= b.before.MinorUnits() {
			return ErrInvalidBet
		}
	} else {
		expected, err := b.before.Subtract(b.request.Money)
		if err != nil {
			return err
		}
		if expected != b.balance || b.failureCode != "" || b.walletVersion < 2 {
			return ErrInvalidBet
		}
	}
	return nil
}

// Apply valida a transição PENDING -> PROCESSED/REJECTED. O chamador mantém o lock SQL.
func (b Bet) Apply(w wallet.Wallet, at time.Time) (Bet, wallet.Wallet, error) {
	if err := b.Validate(); err != nil {
		return Bet{}, wallet.Wallet{}, err
	}
	if b.status != Pending {
		return Bet{}, wallet.Wallet{}, ErrInvalidState
	}
	if err := w.Validate(); err != nil {
		return Bet{}, wallet.Wallet{}, err
	}
	if w.ID() != b.request.WalletID || w.PlayerID() != b.request.PlayerID || w.Balance().Currency() != b.request.Money.Currency() {
		return Bet{}, wallet.Wallet{}, ErrWalletMismatch
	}
	if at.IsZero() || at.Before(b.createdAt) {
		return Bet{}, wallet.Wallet{}, ErrInvalidBet
	}
	// Uma espera pelo lock pode observar uma atualização posterior ao aceite da chamada.
	if at.Before(w.UpdatedAt()) {
		at = w.UpdatedAt()
	}
	moved, err := w.Debit(b.request.Money, at)
	b.before, b.updatedAt = w.Balance(), at.UTC()
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		b.status, b.failureCode, b.balance, b.walletVersion = Rejected, InsufficientFunds, w.Balance(), w.Version()
		return b, w, b.Validate()
	}
	if err != nil {
		return Bet{}, wallet.Wallet{}, err
	}
	b.status, b.balance, b.walletVersion = Processed, moved.Balance(), moved.Version()
	return b, moved, b.Validate()
}

func (b Bet) ID() string                 { return b.id }
func (b Bet) Request() BetRequest        { return b.request }
func (b Bet) Status() string             { return b.status }
func (b Bet) FailureCode() string        { return b.failureCode }
func (b Bet) BalanceBefore() money.Money { return b.before }
func (b Bet) Balance() money.Money       { return b.balance }
func (b Bet) WalletVersion() int64       { return b.walletVersion }
func (b Bet) CreatedAt() time.Time       { return b.createdAt }
func (b Bet) UpdatedAt() time.Time       { return b.updatedAt }
