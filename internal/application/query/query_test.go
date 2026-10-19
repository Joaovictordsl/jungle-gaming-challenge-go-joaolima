package query

import (
	"context"
	"errors"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"strings"
	"testing"
)

type probe struct {
	calls        int
	scope        string
	consistent   bool
	observations int
}

func (p *probe) FindTransaction(_ context.Context, id, scope string) (Transaction, error) {
	p.calls++
	p.scope = scope
	return Transaction{TransactionID: id}, nil
}
func (p *probe) FindExternal(_ context.Context, provider, external string) (Transaction, error) {
	p.calls++
	return Transaction{ProviderID: provider, ExternalTransactionID: external}, nil
}
func (p *probe) ListLedger(_ context.Context, id string, _ Cursor, _ int) (LedgerPage, error) {
	p.calls++
	return LedgerPage{WalletID: id}, nil
}
func (p *probe) Reconcile(_ context.Context, id string) (ReconciliationSnapshot, error) {
	p.calls++
	balance, _ := money.New("100.00", "BRL")
	return ReconciliationSnapshot{WalletID: id, StoredBalance: balance, TotalMinorUnits: "9999", CheckedEntries: 2}, nil
}
func (p *probe) ObserveReconciliation(consistent bool) { p.observations++; p.consistent = consistent }

func TestQueriesValidateScopeAndContextBeforeIO(t *testing.T) {
	p := new(probe)
	service := NewService(p, p)
	ctx := context.Background()
	if _, err := service.FindExternal(ctx, "provider-b", "ext", "provider-a"); !errors.Is(err, ErrForbidden) || p.calls != 0 {
		t.Fatal("Consulta cruzou provedores", err)
	}
	if _, err := service.FindTransaction(ctx, "", ""); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
	if _, err := service.ListLedger(ctx, "w", "bad", 50); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal(err)
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, err := service.ListLedger(ctx, "w", "", limit); !errors.Is(err, ErrInvalidQuery) {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.Reconcile(canceled, "w"); !errors.Is(err, context.Canceled) || p.calls != 0 {
		t.Fatal(err)
	}
	if _, err := service.FindTransaction(ctx, "id", "provider-a"); err != nil || p.scope != "provider-a" {
		t.Fatal(err)
	}
	result, err := service.Reconcile(ctx, "w")
	if err != nil || result.Consistent || result.Difference.Amount != "0.01" || p.observations != 1 || p.consistent {
		t.Fatal("Divergência não observada", err)
	}
}
func TestReconciliationUsesExactWideIntegers(t *testing.T) {
	balance, _ := money.New("100.00", "BRL")
	for _, tc := range []struct {
		total, calculated, difference string
		consistent                    bool
	}{
		{"10000", "100.00", "0.00", true}, {"10001", "100.01", "-0.01", false}, {"9999", "99.99", "0.01", false},
		{"-1", "-0.01", "100.01", false}, {"18446744073709551614", "184467440737095516.14", "-184467440737095416.14", false},
	} {
		result, err := BuildReconciliation(ReconciliationSnapshot{WalletID: "w", StoredBalance: balance, TotalMinorUnits: tc.total, CheckedEntries: 3})
		if err != nil || result.CalculatedBalance.Amount != tc.calculated || result.Difference.Amount != tc.difference || result.Consistent != tc.consistent {
			t.Fatalf("%+v %v", result, err)
		}
	}
}
func TestCursorIsBoundedCanonicalAndWalletScoped(t *testing.T) {
	input := Cursor{Version: 1, WalletID: "w", UpperVersion: 5, AfterVersion: 2, AfterID: "ledger:x"}
	token, err := EncodeCursor(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCursor(token, "w")
	if err != nil || decoded != input {
		t.Fatal(err)
	}
	for _, tc := range []struct{ token, wallet string }{{token, "other"}, {"!", "w"}, {strings.Repeat("a", 2049), "w"}, {token + "=", "w"}} {
		if _, err := DecodeCursor(tc.token, tc.wallet); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal("Cursor inválido aceito", err)
		}
	}
	for _, edit := range []func(*Cursor){func(c *Cursor) { c.Version = 2 }, func(c *Cursor) { c.AfterVersion = 0 }, func(c *Cursor) { c.AfterVersion = c.UpperVersion }, func(c *Cursor) { c.AfterID = "" }} {
		invalid := input
		edit(&invalid)
		if _, err := EncodeCursor(invalid); !errors.Is(err, ErrInvalidCursor) {
			t.Fatal(err)
		}
	}
}
