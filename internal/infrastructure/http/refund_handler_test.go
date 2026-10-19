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

func refundTestRouter(t *testing.T, store refundStoreFunc) *gin.Engine {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r := NewRouter(logger)
	bet := testBetProcessor(func(context.Context, wager.Bet) (wager.Bet, bool, error) {
		t.Fatal("REFUND chegou a BET")
		return wager.Bet{}, false, nil
	})
	win := testWinProcessor(func(context.Context, wager.Win) (wager.Win, bool, error) {
		t.Fatal("REFUND chegou a WIN")
		return wager.Win{}, false, nil
	})
	r.POST("/wagering/transactions", func(c *gin.Context) { c.Set(providerKey, "provider-a") }, NewBetHandler(bet, win, unusedLossProcessor(), testRefundProcessor(store), unusedRollbackProcessor(), logger).Submit)
	return r
}

func TestRefundHTTPContract(t *testing.T) {
	body := strings.Replace(validBetBody, `"kind":"BET"`, `"kind":"REFUND","referenceExternalTransactionId":"bet-original"`, 1)
	for _, tc := range []struct {
		name, mode, part string
		cause            error
		replay           bool
		want             int
	}{
		{name: "sucesso", part: `"balance":{"amount":"100.00","currency":"BRL"}`, want: 200},
		{name: "replay", part: `"idempotentReplay":true`, replay: true, want: 200},
		{name: "aguarda", mode: "pending", part: `"status":"PENDING_REFERENCE"`, want: 202},
		{name: "parcial", mode: "partial", part: "REFERENCE_AMOUNT_MISMATCH", want: 422},
		{name: "duplicada", mode: "duplicate", part: "ALREADY_REVERSED", want: 422},
		{name: "moeda", cause: wager.ErrWalletMismatch, part: "invalid_refund", want: 400},
		{name: "chave", cause: wager.ErrIdempotencyConflict, part: "idempotency_conflict", want: 409},
		{name: "externo", cause: wager.ErrExternalConflict, part: "external_transaction_conflict", want: 409},
		{name: "carteira", cause: wallet.ErrNotFound, part: "wallet_not_found", want: 404},
		{name: "indisponível", cause: wallet.ErrUnavailable, part: "service_unavailable", want: 503},
		{name: "erro", cause: errors.New("private driver details"), part: "internal_error", want: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := refundTestRouter(t, func(ctx context.Context, p wager.Refund) (wager.Refund, bool, error) {
				calls++
				if p.Kind() != wager.RefundKind || p.Request().IdempotencyKey != "caller-key" {
					t.Fatal("Tipo ou chave alterados")
				}
				if tc.cause != nil {
					return wager.Refund{}, false, tc.cause
				}
				balance, _ := money.New("75.00", "BRL")
				current, _ := wallet.NewWallet(p.Request().WalletID, p.Request().PlayerID, balance, p.CreatedAt())
				ref := &wager.Reference{ID: "bet", Kind: wager.BetKind, Status: wager.Processed, Request: p.Request().BetRequest}
				ref.Request.ExternalTransactionID = "bet-original"
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

func TestRefundHTTPRequiresValidPositiveAmountAndReference(t *testing.T) {
	body := strings.Replace(validBetBody, `"kind":"BET"`, `"kind":"REFUND","referenceExternalTransactionId":"bet-original"`, 1)
	for _, invalid := range []string{
		strings.Replace(body, `,"referenceExternalTransactionId":"bet-original"`, "", 1),
		strings.Replace(body, `"bet-original"`, `null`, 1), strings.Replace(body, `"bet-original"`, `""`, 1), strings.Replace(body, `"bet-original"`, `1`, 1),
		strings.Replace(body, `"25.00"`, `"0.00"`, 1), strings.Replace(body, `"25.00"`, `25.00`, 1),
		strings.Replace(body, `"25.00"`, `"-25.00"`, 1), strings.Replace(body, `"BRL"`, `"EUR"`, 1),
		strings.Replace(body, `"kind":"REFUND"`, `"kind":"REFUND","unknown":true`, 1),
		strings.Replace(body, `"bet-original"`, `"bet-original","referenceExternalTransactionId":"bet-original"`, 1),
	} {
		r := refundTestRouter(t, func(context.Context, wager.Refund) (wager.Refund, bool, error) {
			t.Fatal("Entrada inválida chegou ao store")
			return wager.Refund{}, false, nil
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
