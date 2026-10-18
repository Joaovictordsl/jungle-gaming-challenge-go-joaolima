package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
)

type handlerStoreFunc func(context.Context, wallet.Wallet) error

func (f handlerStoreFunc) SaveOpening(ctx context.Context, w wallet.Wallet) error { return f(ctx, w) }

type handlerIDs struct{}

func (handlerIDs) NewID() (string, error) { return "wallet-created", nil }

type handlerClock struct{}

func (handlerClock) Now() time.Time { return time.Date(2026, 10, 18, 13, 30, 0, 0, time.UTC) }

type handlerRepository struct {
	find func(context.Context, string) (wallet.Wallet, error)
}

func (handlerRepository) Create(context.Context, wallet.Wallet) error {
	return errors.New("Create inesperado no transporte")
}
func (r handlerRepository) FindByID(ctx context.Context, id string) (wallet.Wallet, error) {
	if r.find == nil {
		return wallet.Wallet{}, wallet.ErrNotFound
	}
	return r.find(ctx, id)
}
func newHandlerTestOpener(store applicationwallet.OpeningStore) *applicationwallet.OpenWallet {
	return applicationwallet.NewOpenWallet(store, handlerIDs{}, handlerClock{})
}
func handlerTestRouter(open *applicationwallet.OpenWallet, repository wallet.Repository, writer io.Writer) *gin.Engine {
	logger := slog.New(slog.NewJSONHandler(writer, nil))
	handler := NewWalletHandler(open, repository, logger)
	router := NewRouter(logger)
	// Aqui se testa o transporte isolado. A combinação real com o middleware e
	// o PostgreSQL é exercitada pelos testes de integração das rotas.
	router.POST("/wallets", handler.Open)
	router.GET("/wallets/:walletId", handler.FindByID)
	return router
}

func TestOpenWalletHandlerPreservesExactMoneyAndInitialState(t *testing.T) {
	for _, tc := range []struct{ amount, canonical, currency string }{
		{"0.00", "0.00", "BRL"}, {"000100.05", "100.05", "BRL"}, {"92233720368547758.07", "92233720368547758.07", "USD"},
	} {
		t.Run(tc.amount, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			saves := 0
			open := newHandlerTestOpener(handlerStoreFunc(func(got context.Context, w wallet.Wallet) error {
				if got != ctx || w.ID() != "wallet-created" || w.PlayerID() != "player-1" || w.Version() != 1 {
					t.Fatal("Caso de uso não recebeu os dados e o contexto corretos")
				}
				saves++
				return nil
			}))
			router := handlerTestRouter(open, handlerRepository{}, io.Discard)
			body := fmt.Sprintf(`{"playerId":"player-1","initialBalance":{"amount":%q,"currency":%q}}`, tc.amount, tc.currency)
			request := httptest.NewRequest(nethttp.MethodPost, "/wallets", strings.NewReader(body)).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json; charset=utf-8")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var result struct {
				ID, PlayerID string
				Version      int64
				Balance      struct{ Amount, Currency string }
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != nethttp.StatusCreated || saves != 1 || result.ID != "wallet-created" || result.PlayerID != "player-1" || result.Version != 1 || result.Balance.Amount != tc.canonical || result.Balance.Currency != tc.currency {
				t.Fatalf("Abertura não preservou o contrato: %d %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Location") != "/wallets/wallet-created" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("201 deve identificar o recurso criado sem cache")
			}
		})
	}
}

func TestOpenWalletHandlerRejectsInvalidInputBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType, code string
		status                        int
	}{
		{"malformado", `{`, "application/json", "invalid_request", 400},
		{"null", `null`, "application/json", "invalid_request", 400},
		{"sem campos", `{}`, "application/json", "invalid_request", 400},
		{"saldo null", `{"playerId":"p","initialBalance":null}`, "application/json", "invalid_request", 400},
		{"valor numérico", `{"playerId":"p","initialBalance":{"amount":100.25,"currency":"BRL"}}`, "application/json", "invalid_request", 400},
		{"tipo do jogador", `{"playerId":123,"initialBalance":{"amount":"0.00","currency":"BRL"}}`, "application/json", "invalid_request", 400},
		{"jogador null", `{"playerId":null,"initialBalance":{"amount":"0.00","currency":"BRL"}}`, "application/json", "invalid_request", 400},
		{"jogador vazio", `{"playerId":" \t","initialBalance":{"amount":"0.00","currency":"BRL"}}`, "application/json", "invalid_player_id", 400},
		{"campo extra", `{"playerId":"p","version":9,"initialBalance":{"amount":"0.00","currency":"BRL"}}`, "application/json", "invalid_request", 400},
		{"campo extra no saldo", `{"playerId":"p","initialBalance":{"amount":"0.00","currency":"BRL","cents":5}}`, "application/json", "invalid_request", 400},
		{"chave duplicada", `{"playerId":"p","playerId":"other","initialBalance":{"amount":"0.00","currency":"BRL"}}`, "application/json", "invalid_request", 400},
		{"saldo duplicado", `{"playerId":"p","initialBalance":{"amount":"1.00","amount":"2.00","currency":"BRL"}}`, "application/json", "invalid_request", 400},
		{"JSON adicional", `{"playerId":"p","initialBalance":{"amount":"0.00","currency":"BRL"}} {}`, "application/json", "invalid_request", 400},
		{"negativo", `{"playerId":"p","initialBalance":{"amount":"-1.00","currency":"BRL"}}`, "application/json", "invalid_amount", 400},
		{"overflow", `{"playerId":"p","initialBalance":{"amount":"92233720368547758.08","currency":"BRL"}}`, "application/json", "invalid_amount", 400},
		{"moeda inválida", `{"playerId":"p","initialBalance":{"amount":"1.00","currency":"EUR"}}`, "application/json", "invalid_currency", 400},
		{"utf8 inválido", "{\"playerId\":\"\xff\",\"initialBalance\":{\"amount\":\"0.00\",\"currency\":\"BRL\"}}", "application/json", "invalid_request", 400},
		{"mídia inválida", `{}`, "text/plain", "unsupported_media_type", 415},
		{"mídia ausente", `{}`, "", "unsupported_media_type", 415},
		{"corpo grande", strings.Repeat(" ", maxWalletBodyBytes) + `{}`, "application/json", "body_too_large", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := newHandlerTestOpener(handlerStoreFunc(func(context.Context, wallet.Wallet) error {
				t.Fatal("Entrada inválida não pode chegar à persistência")
				return nil
			}))
			router := handlerTestRouter(open, handlerRepository{}, io.Discard)
			request := httptest.NewRequest(nethttp.MethodPost, "/wallets", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.contentType)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || response.Body.String() != `{"error":"`+tc.code+`"}` {
				t.Fatalf("Recebeu %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestOpenWalletHandlerTranslatesPersistenceFailures(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		status int
		code   string
	}{
		{wallet.ErrAlreadyExists, 409, "wallet_already_exists"}, {wallet.ErrUnavailable, 503, "service_unavailable"},
		{context.DeadlineExceeded, 503, "service_unavailable"}, {errors.New("private-database-details"), 500, "internal_error"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			var logs bytes.Buffer
			open := newHandlerTestOpener(handlerStoreFunc(func(context.Context, wallet.Wallet) error { return fmt.Errorf("persistir: %w", tc.cause) }))
			router := handlerTestRouter(open, handlerRepository{}, &logs)
			request := httptest.NewRequest(nethttp.MethodPost, "/wallets", strings.NewReader(`{"playerId":"p","initialBalance":{"amount":"1.00","currency":"BRL"}}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || response.Body.String() != `{"error":"`+tc.code+`"}` {
				t.Fatalf("Falha não foi traduzida: %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(logs.String(), "private-database-details") || strings.Contains(response.Body.String(), "private-database-details") {
				t.Fatal("Detalhes internos vazaram nos logs ou resposta")
			}
		})
	}
}

func TestFindWalletHandlerPreservesPersistedState(t *testing.T) {
	balance, _ := money.New("25.05", "BRL")
	w, err := wallet.Rehydrate("wallet-existing", "player-1", balance, 9, (handlerClock{}).Now(), (handlerClock{}).Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	repository := handlerRepository{find: func(got context.Context, id string) (wallet.Wallet, error) {
		if got != ctx || id != w.ID() {
			t.Fatal("Consulta deve preservar ID e contexto")
		}
		calls++
		return w, nil
	}}
	router := handlerTestRouter(nil, repository, io.Discard)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(nethttp.MethodGet, "/wallets/"+w.ID(), nil).WithContext(ctx))
	if response.Code != 200 || calls != 1 || response.Body.String() != `{"id":"wallet-existing","playerId":"player-1","balance":{"amount":"25.05","currency":"BRL"},"version":9}` {
		t.Fatalf("Consulta alterou o estado: %d %s", response.Code, response.Body.String())
	}
}

func TestFindWalletHandlerTranslatesErrorsAndRejectsInvalidState(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		status int
		code   string
	}{
		{wallet.ErrNotFound, 404, "wallet_not_found"}, {wallet.ErrInvalidID, 400, "invalid_wallet_id"},
		{wallet.ErrUnavailable, 503, "service_unavailable"}, {context.Canceled, 503, "service_unavailable"},
		{errors.New("private-query-details"), 500, "internal_error"}, {nil, 500, "internal_error"},
	} {
		router := handlerTestRouter(nil, handlerRepository{find: func(context.Context, string) (wallet.Wallet, error) { return wallet.Wallet{}, tc.cause }}, io.Discard)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(nethttp.MethodGet, "/wallets/wallet-1", nil))
		if response.Code != tc.status || response.Body.String() != `{"error":"`+tc.code+`"}` {
			t.Fatalf("Consulta deve rejeitar falha/estado inválido: %d %s", response.Code, response.Body.String())
		}
	}
}
