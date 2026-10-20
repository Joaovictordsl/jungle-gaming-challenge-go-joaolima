//go:build oidcsimulation

package oidc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"

	"jungle-gaming-challenge-go-joaolima/internal/application/query"
	applicationwager "jungle-gaming-challenge-go-joaolima/internal/application/wager"
	applicationwallet "jungle-gaming-challenge-go-joaolima/internal/application/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/config"
	"jungle-gaming-challenge-go-joaolima/internal/domain/money"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wager"
	"jungle-gaming-challenge-go-joaolima/internal/domain/wallet"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/auth"
	httpserver "jungle-gaming-challenge-go-joaolima/internal/infrastructure/http"
	"jungle-gaming-challenge-go-joaolima/internal/infrastructure/metrics"
)

const mockURL = "http://127.0.0.1:4900/keycloak"

func mockRequest(t *testing.T, client *http.Client, method, path string, target any) {
	t.Helper()
	request, err := http.NewRequest(method, mockURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("Inicie a simulação Postman na porta 4900 antes dos testes")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Controle da simulação retornou %d", response.StatusCode)
	}
	if target != nil && json.NewDecoder(response.Body).Decode(target) != nil {
		t.Fatal("Resposta inválida da simulação")
	}
}

func simulatedToken(t *testing.T, client *http.Client, kind string) string {
	t.Helper()
	var response struct {
		AccessToken string `json:"access_token"`
	}
	mockRequest(t, client, http.MethodGet, "/__fixtures/token?kind="+kind, &response)
	if response.AccessToken == "" {
		t.Fatal("Fixture não retornou token")
	}
	return response.AccessToken
}

type walletPermissionProbe struct {
	saves atomic.Int32
	bets  atomic.Int32
}

func (p *walletPermissionProbe) ProcessBet(_ context.Context, b wager.Bet) (wager.Bet, bool, error) {
	p.bets.Add(1)
	balance, _ := money.New("100.00", "BRL")
	w, err := wallet.NewWallet(b.Request().WalletID, b.Request().PlayerID, balance, p.Now())
	if err != nil {
		return wager.Bet{}, false, err
	}
	completed, _, err := b.Apply(w, p.Now())
	return completed, false, err
}

func (p *walletPermissionProbe) ProcessWin(_ context.Context, w wager.Win) (wager.Win, bool, error) {
	p.bets.Add(1)
	balance, _ := money.New("100.00", "BRL")
	current, err := wallet.NewWallet(w.Request().WalletID, w.Request().PlayerID, balance, p.Now())
	if err != nil {
		return wager.Win{}, false, err
	}
	completed, _, err := w.Apply(current, nil, p.Now())
	return completed, false, err
}

func (p *walletPermissionProbe) ProcessLoss(_ context.Context, l wager.Loss) (wager.Loss, bool, error) {
	p.bets.Add(1)
	balance, _ := money.New("100.00", "BRL")
	current, err := wallet.NewWallet(l.Request().WalletID, l.Request().PlayerID, balance, p.Now())
	if err != nil {
		return wager.Loss{}, false, err
	}
	result, err := l.Process(current, p.Now())
	return result, false, err
}

func (p *walletPermissionProbe) ProcessRefund(_ context.Context, r wager.Refund) (wager.Refund, bool, error) {
	p.bets.Add(1)
	balance, _ := money.New("100.00", "BRL")
	current, err := wallet.NewWallet(r.Request().WalletID, r.Request().PlayerID, balance, p.Now())
	if err != nil {
		return wager.Refund{}, false, err
	}
	// Referência não disponível nesta fixture: prova a autorização e o aceite 202.
	completed, _, err := r.Apply(current, nil, p.Now())
	return completed, false, err
}

func (p *walletPermissionProbe) ProcessRollback(_ context.Context, r wager.Rollback) (wager.Rollback, bool, error) {
	p.bets.Add(1)
	balance, _ := money.New("100.00", "BRL")
	current, err := wallet.NewWallet(r.Request().WalletID, r.Request().PlayerID, balance, p.Now())
	if err != nil {
		return wager.Rollback{}, false, err
	}
	// Referência não disponível nesta fixture: prova a autorização e o aceite 202.
	completed, _, err := r.Apply(current, nil, p.Now())
	return completed, false, err
}

func (p *walletPermissionProbe) SaveOpening(context.Context, wallet.Wallet) error {
	p.saves.Add(1)
	return nil
}

func (*walletPermissionProbe) NewID() (string, error) { return "permission-wallet", nil }
func (*walletPermissionProbe) Now() time.Time         { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
func (*walletPermissionProbe) Create(context.Context, wallet.Wallet) error {
	return errors.New("Create não participa da abertura HTTP")
}
func (p *walletPermissionProbe) FindByID(_ context.Context, id string) (wallet.Wallet, error) {
	if id != "permission-wallet" {
		return wallet.Wallet{}, wallet.ErrNotFound
	}
	zero, err := money.New("0.00", "BRL")
	if err != nil {
		return wallet.Wallet{}, err
	}
	return wallet.NewWallet(id, "permission-player", zero, p.Now())
}

func (p *walletPermissionProbe) FindTransaction(_ context.Context, id, scope string) (query.Transaction, error) {
	if id != "permission-tx" || (scope != "" && scope != "provider-a") {
		return query.Transaction{}, query.ErrNotFound
	}
	amount, _ := money.New("1.00", "BRL")
	balance, _ := money.Zero("BRL")
	return query.Transaction{TransactionID: id, Origin: "EXTERNAL", Kind: "BET", Status: "PROCESSED", ProviderID: "provider-a", ExternalTransactionID: "bet-1", WalletID: "permission-wallet", PlayerID: "permission-player", RoundID: "round", GameID: "game", Money: amount, Balance: balance, WalletVersion: 2, CreatedAt: p.Now(), UpdatedAt: p.Now()}, nil
}
func (p *walletPermissionProbe) FindExternal(ctx context.Context, provider, external string) (query.Transaction, error) {
	if provider != "provider-a" || external != "bet-1" {
		return query.Transaction{}, query.ErrNotFound
	}
	return p.FindTransaction(ctx, "permission-tx", provider)
}
func (p *walletPermissionProbe) ListLedger(_ context.Context, id string, _ query.Cursor, _ int) (query.LedgerPage, error) {
	return query.LedgerPage{WalletID: id, SnapshotVersion: 1, Entries: []query.LedgerEntry{}}, nil
}
func (p *walletPermissionProbe) Reconcile(_ context.Context, id string) (query.ReconciliationSnapshot, error) {
	balance, _ := money.Zero("BRL")
	return query.ReconciliationSnapshot{WalletID: id, StoredBalance: balance, TotalMinorUnits: "0"}, nil
}

func middlewareApplication(t *testing.T) (*fx.App, *gin.Engine, *walletPermissionProbe) {
	t.Helper()
	var router *gin.Engine
	var middleware *httpserver.Authentication
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := config.Auth{IssuerURL: mockURL + "/realms/jungle", Audience: "jungle-api", InternalClientID: "jungle-internal", HTTPTimeout: 500 * time.Millisecond,
		ProviderClients: map[string]string{"jungle-provider-a": "provider-a", "jungle-provider-b": "provider-b"}}
	application := fx.New(auth.Module, fx.Supply(logger), fx.Replace(cfg),
		fx.Provide(httpserver.NewRouter, httpserver.NewAuthentication), fx.Populate(&router, &middleware), fx.NopLogger)
	if err := application.Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := application.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	router.GET("/protected", middleware.RequireInternalPermission("wallets:write"), func(c *gin.Context) {
		principal, ok := httpserver.AuthenticatedPrincipal(c)
		if !ok || principal.Subject() == "" || principal.ClientID() != "jungle-internal" {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"authorized": true})
	})
	probe := new(walletPermissionProbe)
	opener := applicationwallet.NewOpenWallet(probe, probe, probe)
	httpserver.RegisterWalletRoutes(router, middleware, httpserver.NewWalletHandler(opener, probe, logger))
	httpserver.RegisterBetRoutes(router, middleware, httpserver.NewBetHandler(applicationwager.NewProcessBet(probe, probe, probe), applicationwager.NewProcessWin(probe, probe, probe), applicationwager.NewProcessLoss(probe, probe, probe), applicationwager.NewProcessRefund(probe, probe, probe), applicationwager.NewProcessRollback(probe, probe, probe), metrics.NewMessaging(), logger))
	m := metrics.NewReconciliation()
	httpserver.RegisterQueryRoutes(router, middleware, httpserver.NewQueryHandler(query.NewService(probe, m), m, metrics.NewMessaging(), nil, logger))
	return application, router, probe
}

func walletRequest(t *testing.T, client *http.Client, address, method, token string, want int) {
	t.Helper()
	path, body := "/wallets/permission-wallet", ""
	if method == http.MethodPost {
		path, body = "/wallets", `{"playerId":"permission-player","initialBalance":{"amount":"0.00","currency":"BRL"}}`
	}
	request, err := http.NewRequest(method, address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s %s retornou %d, esperava %d", method, path, response.StatusCode, want)
	}
}

func protectedRequest(t *testing.T, client *http.Client, address, token string, want int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, address+"/protected", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Falha na chamada à API real de teste: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("Middleware retornou %d, esperava %d", response.StatusCode, want)
	}
}

func TestMiddlewareAgainstPostmanOIDCSimulation(t *testing.T) {
	client := &http.Client{Timeout: 3 * time.Second}
	mockRequest(t, client, http.MethodPost, "/__fixtures/reset", nil)
	t.Cleanup(func() { mockRequest(t, client, http.MethodPost, "/__fixtures/reset", nil) })
	application, router, probe := middlewareApplication(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	valid := simulatedToken(t, client, "valid")
	protectedRequest(t, client, server.URL, "", http.StatusUnauthorized)
	protectedRequest(t, client, server.URL, valid, http.StatusOK)
	protectedRequest(t, client, server.URL, valid, http.StatusOK)
	var stats struct{ DiscoveryRequests, JWKSRequests, AuthorizationHeaders int }
	mockRequest(t, client, http.MethodGet, "/__fixtures/stats", &stats)
	if stats.DiscoveryRequests != 1 || stats.JWKSRequests != 1 || stats.AuthorizationHeaders != 0 {
		t.Fatalf("Discovery e JWKS devem ser consultados uma vez, sem encaminhar token: %+v", stats)
	}
	t.Run("permissões das rotas de carteira", func(t *testing.T) {
		readOnly := simulatedToken(t, client, "read-only")
		walletRequest(t, client, server.URL, http.MethodGet, readOnly, http.StatusOK)
		walletRequest(t, client, server.URL, http.MethodPost, readOnly, http.StatusForbidden)
		walletRequest(t, client, server.URL, http.MethodPost, simulatedToken(t, client, "other-client"), http.StatusForbidden)
		if probe.saves.Load() != 0 {
			t.Fatal("Uma requisição sem autorização chegou à persistência")
		}
		walletRequest(t, client, server.URL, http.MethodPost, valid, http.StatusCreated)
		if probe.saves.Load() != 1 {
			t.Fatal("A abertura autorizada deve persistir uma vez")
		}
	})
	t.Run("identidade do provedor antes da aposta e do replay", func(t *testing.T) {
		body := `{"providerId":"provider-a","externalTransactionId":"bet-1","playerId":"permission-player","walletId":"permission-wallet","roundId":"round","gameId":"game","kind":"BET","money":{"amount":"1.00","currency":"BRL"}}`
		for _, operation := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
			operationBody := strings.Replace(body, `"BET"`, `"`+operation+`"`, 1)
			if operation == "LOSS" {
				operationBody = strings.Replace(operationBody, `"1.00"`, `"0.00"`, 1)
			}
			if operation == "REFUND" || operation == "ROLLBACK" {
				operationBody = strings.Replace(operationBody, `"kind":"`+operation+`"`, `"kind":"`+operation+`","referenceExternalTransactionId":"bet-reference"`, 1)
			}
			for _, tc := range []struct {
				kind string
				want int
			}{
				{"valid", 403}, {"unknown-provider", 403}, {"provider-no-role", 403}, {"provider-b", 403}, {"provider-a", 200},
			} {
				token := simulatedToken(t, client, tc.kind)
				request, err := http.NewRequest(http.MethodPost, server.URL+"/wagering/transactions", strings.NewReader(operationBody))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+token)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "bet-1")
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				want := tc.want
				if (operation == "REFUND" || operation == "ROLLBACK") && want == 200 {
					want = 202
				}
				if response.StatusCode != want {
					t.Fatalf("%s %s: status %d, esperado %d", operation, tc.kind, response.StatusCode, want)
				}
			}
		}
		if probe.bets.Load() != 5 {
			t.Fatal("Cliente sem vínculo ou sem papel chegou à persistência")
		}
		walletRequest(t, client, server.URL, http.MethodGet, simulatedToken(t, client, "provider-a"), http.StatusForbidden)
	})
	for _, kind := range []string{"expired", "wrong-issuer", "wrong-audience", "bad-signature", "unsigned", "hmac", "missing-subject", "missing-expiry", "not-before", "future-issued", "id-token"} {
		t.Run(kind, func(t *testing.T) {
			protectedRequest(t, client, server.URL, simulatedToken(t, client, kind), http.StatusUnauthorized)
		})
	}
	for _, kind := range []string{"other-client", "read-only", "wrong-role-audience", "realm-role"} {
		t.Run(kind, func(t *testing.T) {
			protectedRequest(t, client, server.URL, simulatedToken(t, client, kind), http.StatusForbidden)
		})
	}
	response, err := client.Get(server.URL + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("Liveness deve permanecer público")
	}

	// Uma chave já conhecida permite validação local mesmo com o JWKS fora do ar.
	mockRequest(t, client, http.MethodPost, "/__fixtures/failure?jwks=503", nil)
	protectedRequest(t, client, server.URL, valid, http.StatusOK)
	mockRequest(t, client, http.MethodPost, "/__fixtures/rotate", nil)
	rotated := simulatedToken(t, client, "valid")
	protectedRequest(t, client, server.URL, rotated, http.StatusServiceUnavailable)
	mockRequest(t, client, http.MethodPost, "/__fixtures/failure", nil)
	protectedRequest(t, client, server.URL, rotated, http.StatusOK)

	// Uma consulta por chave ainda não conhecida não pode bloquear indefinidamente.
	mockRequest(t, client, http.MethodPost, "/__fixtures/reset", nil)
	mockRequest(t, client, http.MethodPost, "/__fixtures/failure?delay=1500", nil)
	started := time.Now()
	protectedRequest(t, client, server.URL, simulatedToken(t, client, "valid"), http.StatusServiceUnavailable)
	if time.Since(started) > 2*time.Second {
		t.Fatal("Timeout configurado para OIDC não limitou a chamada")
	}
	mockRequest(t, client, http.MethodPost, "/__fixtures/failure?discovery=503", nil)
	failedApplication, _, _ := middlewareApplication(t)
	if err := failedApplication.Start(ctx); !errors.Is(err, auth.ErrUnavailable) {
		t.Fatalf("Discovery indisponível deve impedir inicialização: %v", err)
	}
}

func TestQueriesAuthorizeReadScopeWithOIDCSimulation(t *testing.T) {
	client := &http.Client{Timeout: 2 * time.Second}
	mockRequest(t, client, http.MethodPost, "/__fixtures/reset", nil)
	application, router, _ := middlewareApplication(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	for _, tc := range []struct {
		kind, path, method string
		want               int
	}{
		{"provider-a", "/wagering/transactions/permission-tx", "GET", 200},
		{"provider-read-only", "/wagering/transactions/permission-tx", "GET", 200},
		{"provider-write-only", "/wagering/transactions/permission-tx", "GET", 403},
		{"provider-no-role", "/wagering/transactions/permission-tx", "GET", 403},
		{"unknown-provider", "/wagering/transactions/permission-tx", "GET", 403},
		{"provider-b", "/wagering/transactions/permission-tx", "GET", 404},
		{"provider-a", "/providers/provider-b/wagering/transactions/bet-1", "GET", 403},
		{"provider-a", "/providers/provider-a/wagering/transactions/bet-1", "GET", 200},
		{"valid", "/wagering/transactions/permission-tx", "GET", 200},
		{"read-only", "/wagering/transactions/permission-tx", "GET", 200},
		{"other-client", "/wagering/transactions/permission-tx", "GET", 403},
		{"provider-a", "/wallets/permission-wallet/ledger", "GET", 403},
		{"provider-a", "/wallets/permission-wallet/reconciliation", "POST", 403},
		{"provider-a", "/metrics", "GET", 403},
		{"read-only", "/wallets/permission-wallet/ledger", "GET", 200},
		{"read-only", "/wallets/permission-wallet/reconciliation", "POST", 200},
		{"read-only", "/metrics", "GET", 200},
	} {
		request, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+simulatedToken(t, client, tc.kind))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("%s %s %s: %d esperado %d", tc.kind, tc.method, tc.path, response.StatusCode, tc.want)
		}
	}
}
