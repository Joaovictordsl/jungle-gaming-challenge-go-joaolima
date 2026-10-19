package http

import (
	"context"
	"encoding/json"
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

func TestWinHTTPResultsAndStrictReferenceParsing(t *testing.T) {
	winBody := strings.Replace(validBetBody, `"BET"`, `"WIN"`, 1)
	for _, tc := range []struct {
		name, body, balance string
		want                int
	}{
		{"crédito", winBody, "100.00", 200},
		{"overflow", winBody, "92233720368547758.07", 422},
		{"referência ausente", strings.Replace(winBody, `"kind":"WIN"`, `"kind":"WIN","referenceExternalTransactionId":"bet-missing"`, 1), "100.00", 202},
		{"referência vazia", strings.Replace(winBody, `"kind":"WIN"`, `"kind":"WIN","referenceExternalTransactionId":""`, 1), "100.00", 400},
		{"referência null", strings.Replace(winBody, `"kind":"WIN"`, `"kind":"WIN","referenceExternalTransactionId":null`, 1), "100.00", 400},
		{"referência número", strings.Replace(winBody, `"kind":"WIN"`, `"kind":"WIN","referenceExternalTransactionId":1`, 1), "100.00", 400},
		{"referência duplicada", strings.Replace(winBody, `"kind":"WIN"`, `"kind":"WIN","referenceExternalTransactionId":"a","referenceExternalTransactionId":"b"`, 1), "100.00", 400},
		{"autorreferência", strings.Replace(winBody, `"kind":"WIN"`, `"kind":"WIN","referenceExternalTransactionId":"bet-1"`, 1), "100.00", 400},
		{"zero", strings.Replace(winBody, `"25.00"`, `"0.00"`, 1), "100.00", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			router := NewRouter(logger)
			calls := 0
			win := testWinProcessor(func(ctx context.Context, w wager.Win) (wager.Win, bool, error) {
				calls++
				balance, _ := money.New(tc.balance, "BRL")
				current, _ := wallet.NewWallet(w.Request().WalletID, w.Request().PlayerID, balance, w.CreatedAt())
				result, _, err := w.Apply(current, nil, w.CreatedAt())
				return result, false, err
			})
			bet := testBetProcessor(func(context.Context, wager.Bet) (wager.Bet, bool, error) {
				t.Fatal("WIN chegou ao caso BET")
				return wager.Bet{}, false, nil
			})
			router.POST("/wagering/transactions", func(c *gin.Context) { c.Set(providerKey, "provider-a") }, NewBetHandler(bet, win, unusedLossProcessor(), unusedRefundProcessor(), unusedRollbackProcessor(), logger).Submit)
			request := httptest.NewRequest("POST", "/wagering/transactions", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "win-key")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
			}
			if tc.want == 400 && calls != 0 {
				t.Fatal("Entrada inválida alcançou store")
			}
			if tc.want == 200 {
				var result struct{ Balance struct{ Amount string } }
				if json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Balance.Amount != "125.00" {
					t.Fatal("Resposta não preservou crédito")
				}
			}
		})
	}
}
