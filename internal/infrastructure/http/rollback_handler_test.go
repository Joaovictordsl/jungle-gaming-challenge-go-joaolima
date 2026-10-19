package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

func rollbackTestRouter(t *testing.T, store rollbackStoreFunc) *gin.Engine {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r := NewRouter(logger)
	bet := testBetProcessor(func(context.Context, wager.Bet) (wager.Bet, bool, error) {
		t.Fatal("ROLLBACK chegou a BET")
		return wager.Bet{}, false, nil
	})
	win := testWinProcessor(func(context.Context, wager.Win) (wager.Win, bool, error) {
		t.Fatal("ROLLBACK chegou a WIN")
		return wager.Win{}, false, nil
	})
	r.POST("/wagering/transactions", func(c *gin.Context) { c.Set(providerKey, "provider-a") }, NewBetHandler(bet, win, unusedLossProcessor(), unusedRefundProcessor(), testRollbackProcessor(store), logger).Submit)
	return r
}

func TestRollbackHTTPContract(t *testing.T) {
	body := strings.Replace(validBetBody, `"kind":"BET"`, `"kind":"ROLLBACK","referenceExternalTransactionId":"bet-original"`, 1)
	for _, tc := range []struct {
		name, mode, part string
		cause            error
		replay           bool
		want             int
	}{
		{name: "sucesso", part: `"balance":{"amount":"100.00","currency":"BRL"}`, want: 200},
		{name: "replay", part: `"idempotentReplay":true`, replay: true, want: 200},
		{name: "desfaz prêmio", mode: "win", part: `"balance":{"amount":"50.00","currency":"BRL"}`, want: 200},
		{name: "desfaz refund", mode: "refund", part: `"balance":{"amount":"50.00","currency":"BRL"}`, want: 200},
		{name: "saldo insuficiente", mode: "insufficient", part: "REVERSAL_INSUFFICIENT_FUNDS", want: 422},
		{name: "aguarda", mode: "pending", part: `"status":"PENDING_REFERENCE"`, want: 202},
		{name: "parcial", mode: "partial", part: "REFERENCE_AMOUNT_MISMATCH", want: 422},
		{name: "duplicada", mode: "duplicate", part: "ALREADY_REVERSED", want: 422},
		{name: "moeda", cause: wager.ErrWalletMismatch, part: "invalid_rollback", want: 400},
		{name: "chave", cause: wager.ErrIdempotencyConflict, part: "idempotency_conflict", want: 409},
		{name: "externo", cause: wager.ErrExternalConflict, part: "external_transaction_conflict", want: 409},
		{name: "carteira", cause: wallet.ErrNotFound, part: "wallet_not_found", want: 404},
		{name: "indisponível", cause: wallet.ErrUnavailable, part: "service_unavailable", want: 503},
		{name: "erro", cause: errors.New("private driver details"), part: "internal_error", want: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := rollbackTestRouter(t, func(ctx context.Context, p wager.Rollback) (wager.Rollback, bool, error) {
				calls++
				if p.Kind() != wager.RollbackKind || p.Request().IdempotencyKey != "caller-key" {
					t.Fatal("Tipo ou chave alterados")
				}
				if tc.cause != nil {
					return wager.Rollback{}, false, tc.cause
				}
				balance, _ := money.New("75.00", "BRL")
				current, _ := wallet.NewWallet(p.Request().WalletID, p.Request().PlayerID, balance, p.CreatedAt())
				ref := &wager.Reference{ID: "bet", Kind: wager.BetKind, Status: wager.Processed, Request: p.Request().BetRequest}
				ref.Request.ExternalTransactionID = "bet-original"
				if tc.mode == "win" || tc.mode == "refund" || tc.mode == "insufficient" {
					ref.Kind = wager.WinKind
				}
				if tc.mode == "refund" {
					ref.Kind = wager.RefundKind
				}
				if tc.mode == "insufficient" {
					ref.Request.Money, _ = money.New("25.00", "BRL")
					zero, _ := money.Zero("BRL")
					current, _ = wallet.NewWallet(p.Request().WalletID, p.Request().PlayerID, zero, p.CreatedAt())
				}
				if tc.mode == "pending" {
					ref = nil
				}
				if tc.mode == "partial" {
					ref.Request.Money, _ = money.New("26.00", "BRL")
				}
				if tc.mode == "duplicate" {
					ref.Reversed = true
				}
				result, _, err := p.Apply(current, ref, p.CreatedAt())
				return result, tc.replay, err
			})
			req := httptest.NewRequest("POST", "/wagering/transactions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "caller-key")
			response := httptest.NewRecorder()
			r.ServeHTTP(response, req)
			if response.Code != tc.want || !strings.Contains(response.Body.String(), tc.part) || strings.Contains(response.Body.String(), "private") || calls != 1 {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRollbackHTTPRequiresValidPositiveAmountAndReference(t *testing.T) {
	body := strings.Replace(validBetBody, `"kind":"BET"`, `"kind":"ROLLBACK","referenceExternalTransactionId":"bet-original"`, 1)
	for _, invalid := range []string{
		strings.Replace(body, `,"referenceExternalTransactionId":"bet-original"`, "", 1),
		strings.Replace(body, `"bet-original"`, `"bet-1"`, 1),
		strings.Replace(body, `"bet-original"`, `null`, 1), strings.Replace(body, `"bet-original"`, `""`, 1), strings.Replace(body, `"bet-original"`, `1`, 1),
		strings.Replace(body, `"25.00"`, `"0.00"`, 1), strings.Replace(body, `"25.00"`, `25.00`, 1),
		strings.Replace(body, `"25.00"`, `"-25.00"`, 1), strings.Replace(body, `"BRL"`, `"EUR"`, 1),
		strings.Replace(body, `"kind":"ROLLBACK"`, `"kind":"ROLLBACK","unknown":true`, 1),
		strings.Replace(body, `"bet-original"`, `"bet-original","referenceExternalTransactionId":"bet-original"`, 1),
	} {
		r := rollbackTestRouter(t, func(context.Context, wager.Rollback) (wager.Rollback, bool, error) {
			t.Fatal("Entrada inválida chegou ao store")
			return wager.Rollback{}, false, nil
		})
		req := httptest.NewRequest("POST", "/wagering/transactions", strings.NewReader(invalid))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "key")
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		if response.Code != 400 {
			t.Fatalf("%d %s", response.Code, response.Body.String())
		}
	}
}
