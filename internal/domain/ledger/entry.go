package ledger

import (
	"errors"
	"strings"
	"time"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
)

const (
	Credit = "CREDIT"
	Debit  = "DEBIT"
)

var ErrInvalidEntry = errors.New("lançamento de ledger inválido")

// Entry é um lançamento imutável. Nenhum método altera um lançamento existente.
type Entry struct {
	id, walletID, transactionID, direction string
	amount, before, after                  money.Money
	createdAt                              time.Time
}

func New(id, walletID, transactionID, direction string, amount, before, after money.Money, createdAt time.Time) (Entry, error) {
	e := Entry{id: id, walletID: walletID, transactionID: transactionID, direction: direction,
		amount: amount, before: before, after: after, createdAt: createdAt.UTC()}
	if err := e.Validate(); err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (e Entry) Validate() error {
	if strings.TrimSpace(e.id) == "" || strings.TrimSpace(e.walletID) == "" || strings.TrimSpace(e.transactionID) == "" || e.createdAt.IsZero() {
		return ErrInvalidEntry
	}
	for _, value := range []money.Money{e.amount, e.before, e.after} {
		if err := value.Validate(); err != nil {
			return err
		}
	}
	if e.amount.MinorUnits() <= 0 || e.before.MinorUnits() < 0 || e.after.MinorUnits() < 0 {
		return ErrInvalidEntry
	}
	var expected money.Money
	var err error
	switch e.direction {
	case Credit:
		expected, err = e.before.Add(e.amount)
	case Debit:
		expected, err = e.before.Subtract(e.amount)
	default:
		return ErrInvalidEntry
	}
	if err != nil {
		return err
	}
	comparison, err := expected.Compare(e.after)
	if err != nil {
		return err
	}
	if comparison != 0 {
		return ErrInvalidEntry
	}
	return nil
}

func (e Entry) ID() string                 { return e.id }
func (e Entry) WalletID() string           { return e.walletID }
func (e Entry) TransactionID() string      { return e.transactionID }
func (e Entry) Direction() string          { return e.direction }
func (e Entry) Amount() money.Money        { return e.amount }
func (e Entry) BalanceBefore() money.Money { return e.before }
func (e Entry) BalanceAfter() money.Money  { return e.after }
func (e Entry) CreatedAt() time.Time       { return e.createdAt }
