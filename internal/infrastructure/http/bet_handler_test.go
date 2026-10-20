package http

import (
	"context"
	"errors"
	"io"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	applicationwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

type betStoreFunc func(context.Context, wager.Bet) (wager.Bet, bool, error)

func (f betStoreFunc) ProcessBet(ctx context.Context, b wager.Bet) (wager.Bet, bool, error) {
	return f(ctx, b)
}
func testBetProcessor(store betStoreFunc) *applicationwager.ProcessBet {
	return applicationwager.NewProcessBet(store, handlerIDs{}, handlerClock{})
}

type winStoreFunc func(context.Context, wager.Win) (wager.Win, bool, error)

func (f winStoreFunc) ProcessWin(ctx context.Context, w wager.Win) (wager.Win, bool, error) {
	return f(ctx, w)
}
func testWinProcessor(store winStoreFunc) *applicationwager.ProcessWin {
	return applicationwager.NewProcessWin(store, handlerIDs{}, handlerClock{})
}

type lossStoreFunc func(context.Context, wager.Loss) (wager.Loss, bool, error)

func (f lossStoreFunc) ProcessLoss(ctx context.Context, l wager.Loss) (wager.Loss, bool, error) {
	return f(ctx, l)
}
func testLossProcessor(store lossStoreFunc) *applicationwager.ProcessLoss {
	return applicationwager.NewProcessLoss(store, handlerIDs{}, handlerClock{})
}
func unusedLossProcessor() *applicationwager.ProcessLoss {
	return testLossProcessor(func(context.Context, wager.Loss) (wager.Loss, bool, error) {
		return wager.Loss{}, false, errors.New("LOSS não participa deste teste")
	})
}

type refundStoreFunc func(context.Context, wager.Refund) (wager.Refund, bool, error)

func (f refundStoreFunc) ProcessRefund(ctx context.Context, r wager.Refund) (wager.Refund, bool, error) {
	return f(ctx, r)
}
func testRefundProcessor(store refundStoreFunc) *applicationwager.ProcessRefund {
	return applicationwager.NewProcessRefund(store, handlerIDs{}, handlerClock{})
}
func unusedRefundProcessor() *applicationwager.ProcessRefund {
	return testRefundProcessor(func(context.Context, wager.Refund) (wager.Refund, bool, error) {
		return wager.Refund{}, false, errors.New("REFUND não participa deste teste")
	})
}

const validBetBody = `{"providerId":"provider-a","externalTransactionId":"bet-1","playerId":"player-1","walletId":"wallet-1","roundId":"round-1","gameId":"game-1","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}`

func betTestRouter(store betStoreFunc) *gin.Engine {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := NewRouter(logger)
	// Autorização real é coberta pela simulação OIDC; aqui isolamos o contrato HTTP.
	win := testWinProcessor(func(context.Context, wager.Win) (wager.Win, bool, error) {
		return wager.Win{}, false, errors.New("WIN não participa deste teste BET")
	})
	router.POST("/wagering/transactions", func(c *gin.Context) { c.Set(providerKey, "provider-a") }, NewBetHandler(testBetProcessor(store), win, unusedLossProcessor(), unusedRefundProcessor(), unusedRollbackProcessor(), metrics.NewMessaging(), logger).Submit)
	return router
}

func TestBetHandlerSuccessRejectionAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, amount string
		replay       bool
		cause        error
		status       int
		part         string
	}{
		{"processada", "100.00", false, nil, 200, `"balance":{"amount":"75.00","currency":"BRL"}`},
		{"replay", "100.00", true, nil, 200, `"idempotentReplay":true`},
		{"sem saldo", "0.00", false, nil, 422, `"failureCode":"INSUFFICIENT_FUNDS"`},
		{"conflito chave", "", false, wager.ErrIdempotencyConflict, 409, "idempotency_conflict"},
		{"conflito externo", "", false, wager.ErrExternalConflict, 409, "external_transaction_conflict"},
		{"carteira ausente", "", false, wallet.ErrNotFound, 404, "wallet_not_found"},
		{"carteira incompatível", "", false, wager.ErrWalletMismatch, 400, "invalid_bet"},
		{"indisponível", "", false, wallet.ErrUnavailable, 503, "service_unavailable"},
		{"interno", "", false, errors.New("secret driver error"), 500, "internal_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			router := betTestRouter(func(ctx context.Context, b wager.Bet) (wager.Bet, bool, error) {
				calls++
				if b.Request().IdempotencyKey != "caller-key" {
					t.Fatal("Chave foi substituída")
				}
				if tc.cause != nil {
					return wager.Bet{}, false, tc.cause
				}
				balance, _ := money.New(tc.amount, "BRL")
				w, _ := wallet.NewWallet("wallet-1", "player-1", balance, b.CreatedAt())
				result, _, err := b.Apply(w, b.CreatedAt())
				return result, tc.replay, err
			})
			req := httptest.NewRequest(nethttp.MethodPost, "/wagering/transactions", strings.NewReader(validBetBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "caller-key")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.part) || strings.Contains(response.Body.String(), "secret") || calls != 1 || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBetHandlerRejectsMalformedRequestsBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name, body, key, contentType string
		status                       int
	}{
		{"sem chave", validBetBody, "", "application/json", 400},
		{"chave vazia", validBetBody, " ", "application/json", 400},
		{"chave longa", validBetBody, strings.Repeat("k", 257), "application/json", 400},
		{"sem JSON", validBetBody, "k", "text/plain", 415},
		{"grande", strings.Repeat(" ", maxWalletBodyBytes+1), "k", "application/json", 413},
		{"número monetário", strings.Replace(validBetBody, `"25.00"`, `25.00`, 1), "k", "application/json", 400},
		{"zero", strings.Replace(validBetBody, `"25.00"`, `"0.00"`, 1), "k", "application/json", 400},
		{"negativo", strings.Replace(validBetBody, `"25.00"`, `"-1.00"`, 1), "k", "application/json", 400},
		{"overflow", strings.Replace(validBetBody, `"25.00"`, `"92233720368547758.08"`, 1), "k", "application/json", 400},
		{"interno", strings.Replace(validBetBody, `"BET"`, `"OPENING"`, 1), "k", "application/json", 400},
		{"não implementado", strings.Replace(validBetBody, `"BET"`, `"UNKNOWN"`, 1), "k", "application/json", 400},
		{"provider forjado", strings.Replace(validBetBody, `"provider-a"`, `"provider-b"`, 1), "k", "application/json", 403},
		{"duplicado", strings.Replace(validBetBody, `"kind":"BET"`, `"kind":"BET","kind":"BET"`, 1), "k", "application/json", 400},
		{"extra", validBetBody + `{}`, "k", "application/json", 400},
		{"campo estranho", strings.Replace(validBetBody, `"kind":"BET"`, `"kind":"BET","referenceExternalTransactionId":"x"`, 1), "k", "application/json", 400},
		{"UTF8 inválido", strings.Replace(validBetBody, "game-1", string([]byte{0xff}), 1), "k", "application/json", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := betTestRouter(func(context.Context, wager.Bet) (wager.Bet, bool, error) {
				t.Fatal("Entrada inválida chegou ao banco")
				return wager.Bet{}, false, nil
			})
			req := httptest.NewRequest(nethttp.MethodPost, "/wagering/transactions", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
		})
	}
}

type rollbackStoreFunc func(context.Context, wager.Rollback) (wager.Rollback, bool, error)

func (f rollbackStoreFunc) ProcessRollback(ctx context.Context, r wager.Rollback) (wager.Rollback, bool, error) {
	return f(ctx, r)
}
func testRollbackProcessor(store rollbackStoreFunc) *applicationwager.ProcessRollback {
	return applicationwager.NewProcessRollback(store, handlerIDs{}, handlerClock{})
}
func unusedRollbackProcessor() *applicationwager.ProcessRollback {
	return testRollbackProcessor(func(context.Context, wager.Rollback) (wager.Rollback, bool, error) {
		return wager.Rollback{}, false, errors.New("ROLLBACK não participa deste teste")
	})
}
