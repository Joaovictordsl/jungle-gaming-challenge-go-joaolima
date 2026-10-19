package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"jungle-gaming-challenge-go-joaolima/internal/application/query"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type queryStoreStub struct {
	calls    int
	cause    error
	snapshot query.ReconciliationSnapshot
}

func (s *queryStoreStub) FindTransaction(context.Context, string, string) (query.Transaction, error) {
	s.calls++
	return query.Transaction{}, s.cause
}
func (s *queryStoreStub) FindExternal(context.Context, string, string) (query.Transaction, error) {
	s.calls++
	return query.Transaction{}, s.cause
}
func (s *queryStoreStub) ListLedger(context.Context, string, query.Cursor, int) (query.LedgerPage, error) {
	s.calls++
	return query.LedgerPage{}, s.cause
}
func (s *queryStoreStub) Reconcile(context.Context, string) (query.ReconciliationSnapshot, error) {
	s.calls++
	return s.snapshot, s.cause
}

func TestReconciliationReportsDivergenceInResponseLogsAndMetrics(t *testing.T) {
	balance, _ := money.New("100.00", "BRL")
	s := &queryStoreStub{snapshot: query.ReconciliationSnapshot{WalletID: "w", StoredBalance: balance, TotalMinorUnits: "9999", CheckedEntries: 2}}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	m := metrics.NewReconciliation()
	h := NewQueryHandler(query.NewService(s, m), m, logger)
	router := NewRouter(logger)
	router.POST("/wallets/:walletId/reconciliation", h.Reconcile)
	request := httptest.NewRequest("POST", "/wallets/w/reconciliation", nil)
	request.Header.Set("Authorization", "Bearer private-test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"consistent":false`) || !strings.Contains(response.Body.String(), `"difference":{"amount":"0.01","currency":"BRL"}`) {
		t.Fatal(response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), "Divergência na reconciliação") || strings.Contains(logs.String(), "private-test-token") || strings.Contains(logs.String(), "99.99") {
		t.Fatal("Divergência sem log seguro")
	}
	if !strings.Contains(m.Prometheus(), "jungle_reconciliation_checks_total 1\n") || !strings.Contains(m.Prometheus(), "jungle_reconciliation_divergences_total 1\n") {
		t.Fatal("Métricas incorretas")
	}
}
func newTestQueries() *query.Service {
	return query.NewService(&queryStoreStub{cause: errors.New("query não participa deste teste")}, metrics.NewReconciliation())
}

func TestQueryHandlerRejectsInvalidPaginationBeforeStore(t *testing.T) {
	for _, suffix := range []string{"?limit=0", "?limit=101", "?limit=-1", "?limit=abc", "?limit=", "?limit=1&limit=2", "?cursor=", "?cursor=broken", "?unknown=1", "?limit=%zz"} {
		store := new(queryStoreStub)
		m := metrics.NewReconciliation()
		h := NewQueryHandler(query.NewService(store, m), m, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		r := NewRouter(h.logger)
		r.GET("/wallets/:walletId/ledger", h.Ledger)
		request := httptest.NewRequest("GET", "/wallets/w/ledger"+suffix, nil)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != 400 || store.calls != 0 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", suffix, response.Code, response.Body.String())
		}
	}
}
func TestReadErrorsDoNotExposeDriverDetails(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  int
		part  string
	}{{query.ErrNotFound, 404, "transaction_not_found"}, {query.ErrForbidden, 403, "forbidden"}, {context.DeadlineExceeded, 503, "service_unavailable"}, {errors.New("private database details"), 500, "internal_error"}} {
		s := &queryStoreStub{cause: tc.cause}
		m := metrics.NewReconciliation()
		logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
		h := NewQueryHandler(query.NewService(s, m), m, logger)
		r := NewRouter(logger)
		r.GET("/wagering/transactions/:transactionId", h.FindTransaction)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest("GET", "/wagering/transactions/id", nil))
		if response.Code != tc.want || !strings.Contains(response.Body.String(), tc.part) || strings.Contains(response.Body.String(), "private") {
			t.Fatal(response.Code, response.Body.String())
		}
	}
}
