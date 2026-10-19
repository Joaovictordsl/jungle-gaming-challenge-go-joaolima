package query

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

var (
	ErrInvalidResult = errors.New("resultado de consulta inválido")
	ErrInvalidQuery  = errors.New("consulta inválida")
	ErrInvalidCursor = errors.New("cursor inválido")
	ErrNotFound      = errors.New("transação não encontrada")
	ErrForbidden     = errors.New("consulta não autorizada")
)

// Projeções de leitura: nenhum método reaplica transições financeiras.
type Transaction struct {
	TransactionID                  string      `json:"transactionId"`
	Origin                         string      `json:"origin"`
	Kind                           string      `json:"kind"`
	Status                         string      `json:"status"`
	ProviderID                     string      `json:"providerId,omitempty"`
	ExternalTransactionID          string      `json:"externalTransactionId,omitempty"`
	WalletID                       string      `json:"walletId"`
	PlayerID                       string      `json:"playerId"`
	RoundID                        string      `json:"roundId,omitempty"`
	GameID                         string      `json:"gameId,omitempty"`
	Money                          money.Money `json:"money"`
	Balance                        money.Money `json:"balance"`
	WalletVersion                  int64       `json:"walletVersion"`
	FailureCode                    string      `json:"failureCode,omitempty"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string      `json:"referenceTransactionId,omitempty"`
	CreatedAt                      time.Time   `json:"createdAt"`
	UpdatedAt                      time.Time   `json:"updatedAt"`
}

type LedgerEntry struct {
	ID            string      `json:"id"`
	WalletID      string      `json:"walletId"`
	TransactionID string      `json:"transactionId"`
	Kind          string      `json:"kind"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
	CreatedAt     time.Time   `json:"createdAt"`
}
type LedgerPage struct {
	WalletID        string        `json:"walletId"`
	SnapshotVersion int64         `json:"snapshotVersion"`
	Entries         []LedgerEntry `json:"entries"`
	NextCursor      string        `json:"nextCursor,omitempty"`
}
type ReconciliationSnapshot struct {
	WalletID        string
	StoredBalance   money.Money
	TotalMinorUnits string
	CheckedEntries  int64
}

// Derivações de auditoria usam inteiros sem limite fixo para conseguir reportar
// até divergências corrompidas fora de int64; não são valores de entrada financeira.
type AuditMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}
type Reconciliation struct {
	WalletID          string      `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance AuditMoney  `json:"calculatedBalance"`
	Difference        AuditMoney  `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int64       `json:"checkedEntries"`
}

type Store interface {
	FindTransaction(context.Context, string, string) (Transaction, error)
	FindExternal(context.Context, string, string) (Transaction, error)
	ListLedger(context.Context, string, Cursor, int) (LedgerPage, error)
	Reconcile(context.Context, string) (ReconciliationSnapshot, error)
}
type Observer interface{ ObserveReconciliation(bool) }
type Service struct {
	store    Store
	observer Observer
}

func NewService(store Store, observer Observer) *Service {
	return &Service{store: store, observer: observer}
}

func ValidID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 256 && utf8.ValidString(id)
}

// scope vazio é reservado ao serviço interno, decidido pelo middleware.
func (s *Service) FindTransaction(ctx context.Context, id, scope string) (Transaction, error) {
	if err := ctx.Err(); err != nil {
		return Transaction{}, err
	}
	if !ValidID(id) || (scope != "" && !ValidID(scope)) {
		return Transaction{}, ErrInvalidQuery
	}
	return s.store.FindTransaction(ctx, id, scope)
}
func (s *Service) FindExternal(ctx context.Context, provider, external, scope string) (Transaction, error) {
	if err := ctx.Err(); err != nil {
		return Transaction{}, err
	}
	if !ValidID(provider) || !ValidID(external) {
		return Transaction{}, ErrInvalidQuery
	}
	if scope != "" && scope != provider {
		return Transaction{}, ErrForbidden
	}
	return s.store.FindExternal(ctx, provider, external)
}
func (s *Service) ListLedger(ctx context.Context, id, token string, limit int) (LedgerPage, error) {
	if err := ctx.Err(); err != nil {
		return LedgerPage{}, err
	}
	if !ValidID(id) || limit < 1 || limit > 100 {
		return LedgerPage{}, ErrInvalidQuery
	}
	cursor, err := DecodeCursor(token, id)
	if err != nil {
		return LedgerPage{}, err
	}
	return s.store.ListLedger(ctx, id, cursor, limit)
}
func (s *Service) Reconcile(ctx context.Context, id string) (Reconciliation, error) {
	if err := ctx.Err(); err != nil {
		return Reconciliation{}, err
	}
	if !ValidID(id) {
		return Reconciliation{}, ErrInvalidQuery
	}
	snapshot, err := s.store.Reconcile(ctx, id)
	if err != nil {
		return Reconciliation{}, err
	}
	result, err := BuildReconciliation(snapshot)
	if err != nil {
		return Reconciliation{}, err
	}
	s.observer.ObserveReconciliation(result.Consistent)
	return result, nil
}
func BuildReconciliation(s ReconciliationSnapshot) (Reconciliation, error) {
	if !ValidID(s.WalletID) || s.CheckedEntries < 0 || s.StoredBalance.Validate() != nil || s.StoredBalance.MinorUnits() < 0 {
		return Reconciliation{}, ErrInvalidResult
	}
	calculated, ok := new(big.Int).SetString(s.TotalMinorUnits, 10)
	if !ok {
		return Reconciliation{}, ErrInvalidResult
	}
	stored := big.NewInt(s.StoredBalance.MinorUnits())
	difference := new(big.Int).Sub(stored, calculated)
	return Reconciliation{WalletID: s.WalletID, StoredBalance: s.StoredBalance, CalculatedBalance: AuditMoney{Amount: decimal(calculated), Currency: s.StoredBalance.Currency()}, Difference: AuditMoney{Amount: decimal(difference), Currency: s.StoredBalance.Currency()}, Consistent: difference.Sign() == 0, CheckedEntries: s.CheckedEntries}, nil
}
func decimal(cents *big.Int) string {
	digits := new(big.Int).Abs(cents).String()
	if len(digits) < 3 {
		digits = strings.Repeat("0", 3-len(digits)) + digits
	}
	sign := ""
	if cents.Sign() < 0 {
		sign = "-"
	}
	return sign + digits[:len(digits)-2] + "." + digits[len(digits)-2:]
}
