package http

import (
	"context"
	"errors"
	"io"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func lossTestRouter(t *testing.T, store lossStoreFunc) *gin.Engine {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := NewRouter(logger)
	bet := testBetProcessor(func(context.Context, wager.Bet) (wager.Bet, bool, error) {
		t.Fatal("LOSS chegou ao caso BET")
		return wager.Bet{}, false, nil
	})
	win := testWinProcessor(func(context.Context, wager.Win) (wager.Win, bool, error) {
		t.Fatal("LOSS chegou ao caso WIN")
		return wager.Win{}, false, nil
	})
	router.POST("/wagering/transactions", func(c *gin.Context) { c.Set(providerKey, "provider-a") }, NewBetHandler(bet, win, testLossProcessor(store), unusedRefundProcessor(), unusedRollbackProcessor(), metrics.NewMessaging(), logger).Submit)
	return router
}

func TestLossHTTPContractAndErrors(t *testing.T) {
	body := strings.Replace(strings.Replace(validBetBody, `"BET"`, `"LOSS"`, 1), `"25.00"`, `"0000.00"`, 1)
	for _, tc := range []struct {
		name   string
		replay bool
		cause  error
		want   int
		part   string
	}{
		{"sucesso", false, nil, 200, `"balance":{"amount":"100.00","currency":"BRL"}`},
		{"replay", true, nil, 200, `"idempotentReplay":true`},
		{"moeda incompatível", false, wager.ErrWalletMismatch, 400, "invalid_loss"},
		{"não encontrada", false, wallet.ErrNotFound, 404, "wallet_not_found"},
		{"conflito chave", false, wager.ErrIdempotencyConflict, 409, "idempotency_conflict"},
		{"conflito externo", false, wager.ErrExternalConflict, 409, "external_transaction_conflict"},
		{"indisponível", false, wallet.ErrUnavailable, 503, "service_unavailable"},
		{"interno", false, errors.New("private driver details"), 500, "internal_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			router := lossTestRouter(t, func(ctx context.Context, l wager.Loss) (wager.Loss, bool, error) {
				calls++
				if l.Request().IdempotencyKey != "caller-key" || l.Request().Money.MinorUnits() != 0 {
					t.Fatal("Chave ou zero monetário alterados")
				}
				if tc.cause != nil {
					return wager.Loss{}, false, tc.cause
				}
				balance, _ := money.New("100.00", "BRL")
				current, _ := wallet.NewWallet(l.Request().WalletID, l.Request().PlayerID, balance, l.CreatedAt())
				completed, err := l.Process(current, l.CreatedAt())
				return completed, tc.replay, err
			})
			request := httptest.NewRequest("POST", "/wagering/transactions", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "caller-key")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.want || !strings.Contains(response.Body.String(), tc.part) || strings.Contains(response.Body.String(), "private") || calls != 1 || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLossHTTPRejectsNonZeroAndReferences(t *testing.T) {
	body := strings.Replace(strings.Replace(validBetBody, `"BET"`, `"LOSS"`, 1), `"25.00"`, `"0.00"`, 1)
	for _, invalid := range []string{
		strings.Replace(body, `"0.00"`, `"0.01"`, 1), strings.Replace(body, `"0.00"`, `"-0.00"`, 1),
		strings.Replace(body, `"0.00"`, `0.00`, 1), strings.Replace(body, `"0.00"`, `"0"`, 1), strings.Replace(body, `"0.00"`, `null`, 1),
		strings.Replace(body, `"BRL"`, `"EUR"`, 1),
		strings.Replace(body, `"kind":"LOSS"`, `"kind":"LOSS","referenceExternalTransactionId":"bet-1"`, 1),
		strings.Replace(body, `"kind":"LOSS"`, `"kind":"LOSS","referenceExternalTransactionId":null`, 1),
		strings.Replace(body, `"kind":"LOSS"`, `"kind":"LOSS","referenceExternalTransactionId":""`, 1),
		strings.Replace(body, `"amount":"0.00"`, `"amount":"0.00","amount":"0.00"`, 1),
	} {
		router := lossTestRouter(t, func(context.Context, wager.Loss) (wager.Loss, bool, error) {
			t.Fatal("Entrada inválida alcançou store")
			return wager.Loss{}, false, nil
		})
		request := httptest.NewRequest("POST", "/wagering/transactions", strings.NewReader(invalid))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "key")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("%d %s", response.Code, response.Body.String())
		}
	}
}
