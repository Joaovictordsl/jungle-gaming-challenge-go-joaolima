package wager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"jungle-gaming-challenge-go-joaolima/internal/domain/ledger"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

const (
	WinKind           = "WIN"
	PendingReference  = "PENDING_REFERENCE"
	BalanceOverflow   = "BALANCE_OVERFLOW"
	ReferenceMismatch = "REFERENCE_MISMATCH"
	ReferenceRejected = "REFERENCE_REJECTED"
	ReferenceNotFound = "REFERENCE_NOT_FOUND"
)

var ErrInvalidWin = errors.New("dados do prêmio inválidos")

type WinRequest struct {
	BetRequest
	ReferenceExternalTransactionID string
}

func (r WinRequest) Validate() error {
	if err := r.BetRequest.Validate(); err != nil {
		return errors.Join(ErrInvalidWin, err)
	}
	ref := r.ReferenceExternalTransactionID
	if ref != "" && (strings.TrimSpace(ref) == "" || len(ref) > 256 || !utf8.ValidString(ref) || ref == r.ExternalTransactionID) {
		return ErrInvalidWin
	}
	return nil
}

func (r WinRequest) PayloadHash() (string, error) {
	return r.payloadHash(WinKind)
}

func (r WinRequest) payloadHash(kind string) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	amount, err := r.Money.Amount()
	if err != nil {
		return "", err
	}
	fields := map[string]any{"providerId": r.ProviderID, "externalTransactionId": r.ExternalTransactionID,
		"walletId": r.WalletID, "playerId": r.PlayerID, "roundId": r.RoundID, "gameId": r.GameID,
		"kind": kind, "money": map[string]string{"amount": amount, "currency": r.Money.Currency()}}
	if r.ReferenceExternalTransactionID != "" {
		fields["referenceExternalTransactionId"] = r.ReferenceExternalTransactionID
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Reference é uma projeção de leitura da transação referenciada, sem transições.
type Reference struct {
	ID, Kind, Status string
	Request          BetRequest
	Reversed         bool
}

func (r Reference) Validate() error {
	if strings.TrimSpace(r.ID) == "" || (r.Kind != BetKind && r.Kind != WinKind && r.Kind != LossKind && r.Kind != RefundKind && r.Kind != RollbackKind) ||
		(r.Status != Pending && r.Status != PendingReference && r.Status != Processed && r.Status != Rejected && r.Status != Failed) {
		return ErrInvalidWin
	}
	if r.Kind == LossKind {
		return (LossRequest{BetRequest: r.Request}).Validate()
	}
	return r.Request.Validate()
}

// ReferencedTransaction compartilha as transições de WIN, REFUND e ROLLBACK.
// O tipo é definido pelo construtor e não pode ser alterado depois da criação.
type ReferencedTransaction struct {
	id                               string
	kind                             string
	direction                        string
	request                          WinRequest
	status, failureCode, referenceID string
	before, balance                  money.Money
	walletVersion                    int64
	createdAt, updatedAt             time.Time
}

type Win = ReferencedTransaction
type Refund = ReferencedTransaction
type Rollback = ReferencedTransaction

func NewWin(id string, r WinRequest, at time.Time) (Win, error) {
	w := Win{id: id, kind: WinKind, request: r, status: Pending, createdAt: at.UTC(), updatedAt: at.UTC()}
	if err := w.Validate(); err != nil {
		return Win{}, err
	}
	return w, nil
}
func RehydrateWin(id string, r WinRequest, status, failure, referenceID string, before, balance money.Money, version int64, created, updated time.Time) (Win, error) {
	if status == Pending {
		return Win{}, ErrInvalidState
	}
	w := Win{id: id, kind: WinKind, request: r, status: status, failureCode: failure, referenceID: referenceID, before: before, balance: balance, walletVersion: version, createdAt: created.UTC(), updatedAt: updated.UTC()}
	if err := w.Validate(); err != nil {
		return Win{}, err
	}
	return w, nil
}
func (w ReferencedTransaction) Validate() error {
	if w.kind != WinKind && w.kind != RefundKind && w.kind != RollbackKind {
		return ErrInvalidState
	}
	if strings.TrimSpace(w.id) == "" || w.createdAt.IsZero() || w.updatedAt.Before(w.createdAt) {
		return ErrInvalidWin
	}
	if err := w.request.Validate(); err != nil {
		if w.kind == RollbackKind {
			return errors.Join(ErrInvalidRollback, err)
		}
		return err
	}
	if w.kind == RefundKind {
		if err := (RefundRequest{WinRequest: w.request}).Validate(); err != nil {
			return err
		}
	}
	if w.kind == RollbackKind {
		if err := (RollbackRequest{WinRequest: w.request}).Validate(); err != nil {
			return err
		}
		if w.direction != "" && w.direction != ledger.Credit && w.direction != ledger.Debit {
			return ErrInvalidRollback
		}
	} else if w.direction != "" {
		return ErrInvalidState
	}
	if w.status == Pending {
		if w.direction != "" || w.failureCode != "" || w.referenceID != "" || w.walletVersion != 0 || w.before != (money.Money{}) || w.balance != (money.Money{}) {
			return ErrInvalidWin
		}
		return nil
	}
	for _, m := range []money.Money{w.before, w.balance} {
		if err := m.Validate(); err != nil {
			return err
		}
		if m.MinorUnits() < 0 || m.Currency() != w.request.Money.Currency() {
			return ErrInvalidWin
		}
	}
	if w.walletVersion < 1 {
		return ErrInvalidWin
	}
	if w.request.ReferenceExternalTransactionID == "" && w.referenceID != "" {
		return ErrInvalidWin
	}
	switch w.status {
	case Failed:
		if w.failureCode != InfrastructureRetriesExhausted || w.before != w.balance || w.direction != "" || w.referenceID != "" {
			return ErrInvalidWin
		}
	case PendingReference:
		if w.request.ReferenceExternalTransactionID == "" || w.referenceID != "" || w.failureCode != "" || w.before != w.balance || w.direction != "" {
			return ErrInvalidWin
		}
	case Processed:
		if w.kind == RollbackKind && w.direction == "" {
			return ErrInvalidRollback
		}
		expected, err := w.before.Add(w.request.Money)
		if w.Direction() == ledger.Debit {
			expected, err = w.before.Subtract(w.request.Money)
		}
		if err != nil {
			return err
		}
		if expected != w.balance || w.walletVersion < 2 || w.failureCode != "" || (w.request.ReferenceExternalTransactionID != "" && w.referenceID == "") {
			return ErrInvalidWin
		}
	case Rejected:
		if w.before != w.balance {
			return ErrInvalidWin
		}
		if w.kind == RollbackKind {
			needsDirection := w.failureCode == AlreadyReversed || w.failureCode == BalanceOverflow || w.failureCode == ReversalInsufficientFunds
			if needsDirection != (w.direction != "") {
				return ErrInvalidRollback
			}
		}
		switch w.failureCode {
		case BalanceOverflow:
			_, err := w.before.Add(w.request.Money)
			if w.Direction() != ledger.Credit || !errors.Is(err, money.ErrOverflow) || (w.request.ReferenceExternalTransactionID != "" && w.referenceID == "") {
				return ErrInvalidWin
			}
		case ReferenceMismatch, ReferenceRejected:
			if w.request.ReferenceExternalTransactionID == "" || w.referenceID == "" {
				return ErrInvalidWin
			}
		case ReversalInsufficientFunds:
			if w.kind != RollbackKind || w.Direction() != ledger.Debit || w.referenceID == "" || w.before.MinorUnits() >= w.request.Money.MinorUnits() {
				return ErrInvalidRollback
			}
		case ReferenceAmountMismatch, AlreadyReversed:
			if (w.kind != RefundKind && w.kind != RollbackKind) || w.referenceID == "" {
				return ErrInvalidRefund
			}
		case ReferenceNotFound:
			if w.request.ReferenceExternalTransactionID == "" || w.referenceID != "" {
				return ErrInvalidWin
			}
		default:
			return ErrInvalidWin
		}
	default:
		return ErrInvalidState
	}
	return nil
}

func (w ReferencedTransaction) observe(current wallet.Wallet, at time.Time) (ReferencedTransaction, error) {
	if err := w.Validate(); err != nil {
		return Win{}, err
	}
	if w.status != Pending && w.status != PendingReference {
		return Win{}, ErrInvalidState
	}
	if err := current.Validate(); err != nil {
		return Win{}, err
	}
	if current.ID() != w.request.WalletID || current.PlayerID() != w.request.PlayerID || current.Balance().Currency() != w.request.Money.Currency() {
		return Win{}, ErrWalletMismatch
	}
	if at.IsZero() || at.Before(w.updatedAt) {
		return Win{}, ErrInvalidWin
	}
	if at.Before(current.UpdatedAt()) {
		at = current.UpdatedAt()
	}
	w.before, w.balance, w.walletVersion, w.updatedAt = current.Balance(), current.Balance(), current.Version(), at.UTC()
	return w, nil
}

// Apply resolve a referência e cria uma nova movimentação, sem editar a original.
func (w ReferencedTransaction) Apply(current wallet.Wallet, reference *Reference, at time.Time) (ReferencedTransaction, wallet.Wallet, error) {
	observed, err := w.observe(current, at)
	if err != nil {
		return Win{}, wallet.Wallet{}, err
	}
	w = observed
	if w.request.ReferenceExternalTransactionID != "" {
		if reference == nil {
			w.status = PendingReference
			return w, current, w.Validate()
		}
		if err := reference.Validate(); err != nil {
			return Win{}, wallet.Wallet{}, err
		}
		r := reference.Request
		w.referenceID = reference.ID
		validKind := reference.Kind == BetKind
		if w.kind == RollbackKind {
			validKind = validKind || reference.Kind == WinKind || reference.Kind == RefundKind
		}
		if !validKind || r.ProviderID != w.request.ProviderID || r.ExternalTransactionID != w.request.ReferenceExternalTransactionID ||
			r.WalletID != w.request.WalletID || r.PlayerID != w.request.PlayerID || r.RoundID != w.request.RoundID || r.GameID != w.request.GameID || r.Money.Currency() != w.request.Money.Currency() {
			w.status, w.failureCode = Rejected, ReferenceMismatch
			return w, current, w.Validate()
		}
		if reference.Status == Pending || reference.Status == PendingReference {
			w.referenceID = ""
			w.status = PendingReference
			return w, current, w.Validate()
		}
		if reference.Status != Processed {
			w.status, w.failureCode = Rejected, ReferenceRejected
			return w, current, w.Validate()
		}
		if (w.kind == RefundKind || w.kind == RollbackKind) && r.Money != w.request.Money {
			w.status, w.failureCode = Rejected, ReferenceAmountMismatch
			return w, current, w.Validate()
		}
		if w.kind == RollbackKind {
			w.direction = ledger.Debit
			if reference.Kind == BetKind {
				w.direction = ledger.Credit
			}
		}
		if (w.kind == RefundKind || w.kind == RollbackKind) && reference.Reversed {
			w.status, w.failureCode = Rejected, AlreadyReversed
			return w, current, w.Validate()
		}
	}
	var moved wallet.Wallet
	if w.Direction() == ledger.Debit {
		moved, err = current.Debit(w.request.Money, w.updatedAt)
	} else {
		moved, err = current.Credit(w.request.Money, w.updatedAt)
	}
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		w.status, w.failureCode = Rejected, ReversalInsufficientFunds
		return w, current, w.Validate()
	}
	if errors.Is(err, money.ErrOverflow) {
		w.status, w.failureCode = Rejected, BalanceOverflow
		return w, current, w.Validate()
	}
	if err != nil {
		return Win{}, wallet.Wallet{}, err
	}
	w.status, w.balance, w.walletVersion = Processed, moved.Balance(), moved.Version()
	return w, moved, w.Validate()
}

func (w ReferencedTransaction) ExpireReference(current wallet.Wallet, at time.Time) (ReferencedTransaction, error) {
	if w.status != PendingReference {
		return Win{}, ErrInvalidState
	}
	observed, err := w.observe(current, at)
	if err != nil {
		return Win{}, err
	}
	observed.status, observed.failureCode = Rejected, ReferenceNotFound
	return observed, observed.Validate()
}

func (w ReferencedTransaction) ID() string   { return w.id }
func (w ReferencedTransaction) Kind() string { return w.kind }
func (w ReferencedTransaction) PayloadHash() (string, error) {
	if err := w.Validate(); err != nil {
		return "", err
	}
	if w.kind == RefundKind {
		return (RefundRequest{WinRequest: w.request}).PayloadHash()
	}
	if w.kind == RollbackKind {
		return (RollbackRequest{WinRequest: w.request}).PayloadHash()
	}
	return w.request.PayloadHash()
}
func (w ReferencedTransaction) Request() WinRequest        { return w.request }
func (w ReferencedTransaction) Status() string             { return w.status }
func (w ReferencedTransaction) FailureCode() string        { return w.failureCode }
func (w ReferencedTransaction) ReferenceID() string        { return w.referenceID }
func (w ReferencedTransaction) BalanceBefore() money.Money { return w.before }
func (w ReferencedTransaction) Balance() money.Money       { return w.balance }
func (w ReferencedTransaction) WalletVersion() int64       { return w.walletVersion }
func (w ReferencedTransaction) CreatedAt() time.Time       { return w.createdAt }
func (w ReferencedTransaction) UpdatedAt() time.Time       { return w.updatedAt }

// Direction é determinada pela referência, nunca pelo corpo da requisição.
func (w ReferencedTransaction) Direction() string {
	if w.kind == RollbackKind {
		return w.direction
	}
	return ledger.Credit
}
